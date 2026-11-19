package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Item описывает товары в магазине
type Item struct {
	ID    int     `json:"id"`
	Name  string  `json:"name"`
	Price float64 `json:"price"`
}

// Store хранит данные
type Store struct {
	mu    sync.RWMutex
	items map[int]Item
}

// Глобальные переменные
var (
	store  = Store{items: make(map[int]Item)}
	nextID atomic.Int64 //генерация уникального ID
)

// функция для очистки экрана (работает и на Windows, и на macOS/Linux)
func clear_screen() {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/c", "cls")
	} else {
		cmd = exec.Command("clear")
	}
	cmd.Stdout = os.Stdout
	cmd.Run()
}

// readString читает строку и убирает лишние пробелы и переносы строк
func readString(reader *bufio.Reader, prompt string) string {
	fmt.Print(prompt + " ")
	text, _ := reader.ReadString('\n')
	return strings.TrimSpace(text)
}

// readInt читает число из консоли
func readInt(reader *bufio.Reader, prompt string) (int, error) {
	text := readString(reader, prompt)
	return strconv.Atoi(text)
}

// readFloat читает число с плавающей точкой из консоли
func readFloat(reader *bufio.Reader, prompt string) (float64, error) {
	text := readString(reader, prompt)
	return strconv.ParseFloat(text, 64)
}

func handleGetItems(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		sendJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	store.mu.RLock()
	defer store.mu.RUnlock()

	itemsList := make([]Item, 0, len(store.items))
	for _, item := range store.items {
		itemsList = append(itemsList, item)
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(itemsList); err != nil {
		// тело уже пошло клиенту, статус менять поздно — только логируем
		log.Printf("encode error: %v", err)
	}
}
func handleGetItem(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id") // Получение id из пути
	id, err := strconv.Atoi(idStr)
	if err != nil {
		sendJSONError(w, "Invalid ID format", http.StatusBadRequest)
		return
	}

	store.mu.RLock()
	item, exists := store.items[id]
	store.mu.RUnlock()
	if !exists {
		sendJSONError(w, "Item not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK) // Явно указываем статус 200 OK
	json.NewEncoder(w).Encode(item)
}
func handleAddItem(w http.ResponseWriter, r *http.Request) {
	// mime.ParseMediaType корректно обрабатывает "application/json; charset=utf-8"
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		sendJSONError(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return
	}

	var newItem Item
	// битый или пустой JSON теперь возвращает 400, а не 500
	if err := json.NewDecoder(r.Body).Decode(&newItem); err != nil {
		sendJSONError(w, "Invalid or empty JSON body", http.StatusBadRequest)
		return
	}

	if newItem.Name == "" {
		sendJSONError(w, "Name is required", http.StatusBadRequest)
		return
	}

	// запрет отрицательной цены
	if newItem.Price < 0 {
		sendJSONError(w, "Price cannot be negative", http.StatusBadRequest)
		return
	}

	newID := int(nextID.Add(1))

	// Сохраняем товар в локальную переменную, чтобы избежать гонки
	createdItem := Item{
		ID:    newID,
		Name:  newItem.Name,
		Price: newItem.Price,
	}

	store.mu.Lock()
	store.items[newID] = createdItem
	store.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(createdItem)
}
func handleDeleteItem(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		sendJSONError(w, "Invalid ID format", http.StatusBadRequest)
		return
	}

	store.mu.Lock()
	_, exists := store.items[id]
	if exists {
		delete(store.items, id) //Удаляем элемент по ключу
	}
	store.mu.Unlock()

	if !exists {
		sendJSONError(w, "Item not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"message": "Item deleted successfully"})
}
func sendJSONError(w http.ResponseWriter, message string, statusCode int) {
	w.Header().Set("Content-Type", "application/json") //Даже при ошибке говорим, что это JSON
	w.WriteHeader(statusCode)                          //Устанавливаем код ошибки (400, 404, 415 и т.д.)
	//Создаём анонимную структуру на лету и кодируем её в JSON
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// перехватывает стандартные ТЕКСТОВЫЕ ответы mux (404, 405)
// и подменяет их на JSON. Ответы наших обработчиков не трогает.
type jsonErrorWrapper struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	hijacked    bool
}

func (w *jsonErrorWrapper) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.status = status

	isMuxError := status == http.StatusNotFound || status == http.StatusMethodNotAllowed
	alreadyJSON := strings.HasPrefix(w.Header().Get("Content-Type"), "application/json")

	if isMuxError && !alreadyJSON {
		msg := "not found"
		if status == http.StatusMethodNotAllowed {
			msg = "method not allowed"
		}
		w.Header().Set("Content-Type", "application/json")
		w.ResponseWriter.WriteHeader(status)
		json.NewEncoder(w.ResponseWriter).Encode(map[string]string{"error": msg})
		w.hijacked = true // дальше всё, что напишет mux, будем выбрасывать
		return
	}

	w.ResponseWriter.WriteHeader(status)
}

func (w *jsonErrorWrapper) Write(b []byte) (int, error) {
	if !w.wroteHeader { // если Write вызван раньше WriteHeader — статус 200
		w.WriteHeader(http.StatusOK)
	}
	if w.hijacked {
		// mux пытается дописать "404 page not found\n" поверх нашего JSON.
		// Делаем вид, что записали, но байты выбрасываем.
		return len(b), nil
	}
	return w.ResponseWriter.Write(b)
}

func jsonErrorMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&jsonErrorWrapper{ResponseWriter: w}, r)
	})
}

func main() {
	addr := flag.String("addr", ":8080", "HTTP Server address")

	if envPort := os.Getenv("PORT"); envPort != "" {
		*addr = ":" + envPort
	}
	flag.Parse()

	mux := http.NewServeMux() // Мультиплексор запросов

	mux.HandleFunc("GET /items", handleGetItems)
	mux.HandleFunc("GET /items/{id}", handleGetItem)
	mux.HandleFunc("POST /items", handleAddItem)
	mux.HandleFunc("DELETE /items/{id}", handleDeleteItem)

	//Конфиг сервера
	server := &http.Server{
		Addr:              *addr,
		Handler:           jsonErrorMiddleware(mux),
		ReadHeaderTimeout: 5 * time.Second,  // Защита от медленных атак
		WriteTimeout:      10 * time.Second, // Максимальное время на формирование ответа
	}

	go func() {
		log.Printf("Сервер запущен по адресу: http://localhost%s", *addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Ошибка сервера: %v", err)
		}
	}()

	reader := bufio.NewReader(os.Stdin)
	baseURL := "http://localhost" + *addr
	for {
		fmt.Println("\nДля продолжения нажмите Enter...")
		if _, err := reader.ReadString('\n'); err != nil {
			return // ввод закончился (Ctrl+D) — выходим, а не крутимся вечно
		}
		clear_screen()
		fmt.Println("Меню команд:")
		fmt.Println("1 - Получить список товаров")
		fmt.Println("2 - Получить один товар")
		fmt.Println("3 - Добавить товар")
		fmt.Println("4 - Удалить товар")
		fmt.Println("5 - Выполнить произвольную команду")
		fmt.Println("0 - Завершить работу")

		a, err := readInt(reader, "Выберите пункт из меню управления:")
		if err != nil {
			fmt.Println(err)
			continue
		}
		switch a {
		case 0:
			fmt.Println("Завершение работы...")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := server.Shutdown(ctx); err != nil {
				log.Printf("Ошибка при остановке: %v", err)
			}
			return
		case 1:
			resp, err := http.Get(baseURL + "/items")
			if err != nil {
				fmt.Printf("Ошибка запроса: %v\n", err)
			} else {
				printResponse(resp)
			}

		case 2:
			id := readString(reader, "Введите ID товара:")
			resp, err := http.Get(baseURL + "/items/" + id)
			if err != nil {
				fmt.Printf("Ошибка запроса: %v\n", err)
			} else {
				printResponse(resp)
			}

		case 3:
			name := readString(reader, "Введите название товара:")
			price, err := readFloat(reader, "Введите цену товара:")
			if err != nil {
				fmt.Println("Ошибка: некорректная цена!")
				time.Sleep(1 * time.Second)
				continue
			}

			// Формируем JSON и отправляем POST-запрос
			newItem := Item{Name: name, Price: price}
			jsonData, _ := json.Marshal(newItem)

			resp, err := http.Post(baseURL+"/items", "application/json", bytes.NewBuffer(jsonData))
			if err != nil {
				fmt.Printf("Ошибка запроса: %v\n", err)
			} else {
				printResponse(resp)
			}

		case 4:
			id := readString(reader, "Введите ID товара для удаления:")

			// Для DELETE создаём запрос вручную
			req, err := http.NewRequest(http.MethodDelete, baseURL+"/items/"+id, nil)
			if err != nil {
				fmt.Printf("Ошибка создания запроса: %v\n", err)
				continue
			}

			client := &http.Client{}
			resp, err := client.Do(req)
			if err != nil {
				fmt.Printf("Ошибка запроса: %v\n", err)
			} else {
				printResponse(resp)
			}
		case 5:
			cmd := readString(reader, "Введите команду:")
			if cmd == "" {
				continue
			}
			var execCmd *exec.Cmd
			if runtime.GOOS == "windows" {

				execCmd = exec.Command("powershell", "-Command", cmd)
			} else {
				execCmd = exec.Command("sh", "-c", cmd)
			}
			execCmd.Stdout = os.Stdout
			execCmd.Stderr = os.Stderr
			if err := execCmd.Run(); err != nil {
				fmt.Printf("Ошибка выполнения: %v\n", err)
			}
		default:
			fmt.Println("Неверный пункт меню. Попробуйте снова.")
			time.Sleep(1 * time.Second)
		}

	}
}

// Функция для ответа в виде JSON
func printResponse(resp *http.Response) {
	defer resp.Body.Close() // Обязательное закрытие тела ответа

	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)

	fmt.Printf("\n Статус ответа: %s\n", resp.Status)

	// Пытаемся отформатировать JSON с отступами для красоты
	var prettyJSON bytes.Buffer
	if json.Indent(&prettyJSON, buf.Bytes(), "", "  ") == nil {
		fmt.Println(prettyJSON.String())
	} else {
		fmt.Println(buf.String()) // Если не JSON, выводим как есть
	}
}
