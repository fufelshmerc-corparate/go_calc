# Распределённый вычислитель арифметических выражений

Система для параллельного вычисления сложных арифметических выражений с использованием оркестратора и агентов-вычислителей.

## Установка и настройка

1. **Клонируйте репозиторий**:
    ```
    git clone https://github.com/IvanSolomatin/calc_v2
    cd calc_go
    ```

2. **Убедитесь, что установлен go 1.23.1+**:
    ```
    go version
    ```


---

## Архитектура

```mermaid
graph TD
    U[Пользователь] -->|POST /calculate| O[Оркестратор]
    U -->|GET /expressions| O
    O -->|GET /internal/task| A1[Агент 1]
    O -->|GET /internal/task| A2[Агент 2]
    O -->|GET /internal/task| A3[Агент 3]
    A1 -->|POST /internal/task| O
    A2 -->|POST /internal/task| O
    A3 -->|POST /internal/task| O
```

**Оркестратор** :

- Принимает выражения через REST API
- Разбивает выражения на простые задачи
- Управляет очередью задач
- Собирает результаты
- Хранит вычисления

**Агенты**:

- Получают задачи от оркестратора
- Выполняют арифметические операции с задержкой(задается пользователем через переменные среды)
- Возвращают результаты оркестратору

## Возможности

- Поддерживаемые операции: `+`, `-`, `*`, `/`
- Приоритет операций и скобки
- Параллельное выполнение операций

# Запуск 

go run cmd/main.go

## Windows (cmd.exe)


### Установка переменных среды

set PORT= 8082 //порт
set COMPUTING_POWER=1 //количество агентов
set TIME_ADDITION_MS=200 //время сложения
set TIME_SUBTRACTION_MS=200 //время вычитания
set TIME_MULTIPLICATIONS_MS=200 //время умножения
set TIME_DIVISIONS_MS=200 //время деления
здесь пример значений которые установлены по умолчанию


## API Endpoints

### 1. Добавление выражения

Пример запроса:

```bash
curl --location 'http://localhost:8082/api/v1/calculate' \
--header 'Content-Type: application/json' \
--data '{
  "expression": "(2+3)*4-10/2"
}'
```

Успешный ответ (201):

```json
{
    "id": "1"
}
```

### 2. Получение списка выражений

Пример ответа (200):

```json
{
    "expressions": [
        {
            "id": "1",
            "expression": "(2+3)*4-10/2",
            "status": "completed",
            "result": 15
        },
        {
            "id": "2",
            "expression": "8/(4-4)",
            "status": "error",
        }
    ]
}
```

### 3. Получение выражения по ID

Пример запроса:

```bash
curl http://localhost:8082/api/v1/expressions/1
```

Ответ (200):

```json
{
    "id": "1",
    "expression": "2*2+2*2+2*2+2*2+2*2+2*2+2*2+2*2+2*2+2*2+2*2+2*2+2*2+2*2+2*2+2*2+2*2+2*2+2*2+2*2+2*2+2*2+2*2",
    "status": "completed",
    "result": 92
}

{
    "id": "2",
    "expression": "2*2+2*2+2*2+2*2+2*2+2*2+2*2+2*2+2*2+2*2+2*2+2*2+2*2+2*2+2*2+2*2+2*2+2*2+2*2+2*2+2*2+2*2+2*2",
    "status": "processing"
}
```

## Внутреннее API (для агентов, не для пользователя)

### 1. Получение задачи

```curl
curl http://localhost:8082/internal/task
```

Пример ответа (200):

```json
{
    "id":"1",
    "arg1":2,
    "arg2":2,
    "operation":"*",
    "operationtime":100
}
```

### 2. Отправка результата

```curl
curl -X POST "http://localhost:8082/internal/task" ^ -H "Content-Type: application/json" ^ -d "{"id": "1", "result": 5}"  
```

Пример запроса:

```json
{
  "id": "1",
  "result": 4
}
```


## Примеры сценариев

### Сценарий 1: Успешное вычисление

```cmd
curl -X POST "http://localhost:8082/api/v1/calculate" ^ -H "Content-Type: application/json" ^ -d "{"expression": "1/1"}"
```
# Проверка статуса
curl http://localhost:8080/api/v1/expressions

# Ответ 
```
{
    "expression": {
        "id": "1",
        "status": "completed",
        "result": 1
    }
}
```

### Сценарий 2: Ошибка деления на ноль

```curl
curl -X POST "http://localhost:8082/api/v1/calculate" ^ -H "Content-Type: application/json" ^ -d "{"expression": "1/0"}"
```

# Ответ:
```
{
    "expression": {
        "id": "2",
        "status": "error",
    }
}
```

## Тестирование
Перед тестированием надо убрать переменные среды, так как тест рассчитан(по времени) на работу с стандартными значениями

```cmd
go test .\internal\app\ -v
```