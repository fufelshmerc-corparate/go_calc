


$env:PORT= 1
go run .\orchestrator.go





$env:PORT= 1
$env:COMPUTING_POWER=10
go run agent.go
$env:COMPUTING_POWER=1
$env:TIME_TIMEOUT_MS=100 
$env:TIME_ADDITION_MS=100
$env:TIME_SUBTRACTION_MS=100
$env:TIME_MULTIPLICATIONS_MS=100
$env:TIME_DIVISIONS_MS=100
$env:TIME_TIMEOUT_MS=100
go run .\main.go





curl -X POST "http://localhost:8082/api/v1/calculate" ^ -H "Content-Type: application/json" ^ -d "{\"expression\": \"2*2+2*2+2*2+2*2+2*2+2*2+2*2+2*2\"}"

curl -X GET "http://localhost:8082/api/v1/expressions"

curl -X GET "localhost:8082/internal/task"