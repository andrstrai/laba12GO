Команды для ввода:
curl.exe -i http://localhost:8080/items
curl.exe -i http://localhost:8080/items/1
curl.exe -i -X POST http://localhost:8080/items -H "Content-Type: application/json" -d '{\"name\": \"Ноутбук\", \"price\": 55000.0}'
curl.exe -i -X DELETE http://localhost:8080/items/1
