package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)


func getEnvDuration(envVar string, defaultValue int) time.Duration {
	value, err := strconv.Atoi(os.Getenv(envVar))
	if err != nil {
		return time.Duration(defaultValue) * time.Millisecond
	}
	return time.Duration(value) * time.Millisecond
}

func getServerURL() string {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8082"
	}
	return "http://localhost:" + port
}

func getTask() (*Task, error) {
	
	client := http.DefaultClient

	resp, err := client.Get(getServerURL() + "/internal/task")
	if err != nil {
		return nil, fmt.Errorf("ошибка запроса задачи: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("нет доступных задач")
	}

	var task Task
	if err := json.NewDecoder(resp.Body).Decode(&task); err != nil {
		return nil, fmt.Errorf("ошибка декодирования задачи: %v", err)
	}
	return &task, nil
}

func performOperation(task *Task) (float64, error) {
	
	var result float64
	var err error
	switch task.Operation {
	case "+":
		time.Sleep(time.Duration(task.Operationtime)*time.Millisecond)
		result = task.Arg1 + task.Arg2
	case "-":
		time.Sleep(time.Duration(task.Operationtime)*time.Millisecond)
		result = task.Arg1 - task.Arg2
	case "*":
		time.Sleep(time.Duration(task.Operationtime)*time.Millisecond)
		result = task.Arg1 * task.Arg2
	case "/":
		time.Sleep(time.Duration(task.Operationtime)*time.Millisecond)
		if task.Arg2 == 0 {
			err = fmt.Errorf("деление на ноль")
		} else {
			result = task.Arg1 / task.Arg2
		}
	default:
		err = fmt.Errorf("неизвестная операция: %s", task.Operation)
	}
	return result, err
}

func postResult(result *Result) error {
	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("ошибка сериализации результата: %v", err)
	}

	resp, err := http.Post(getServerURL()+"/internal/task", "application/json", bytes.NewBuffer(data))
	if err != nil {
		return fmt.Errorf("ошибка отправки результата: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("сервер вернул ошибку: %v", resp.Status)
	}
	return nil
}

func clientWorker(wg *sync.WaitGroup) {
	defer wg.Done()
	timeout := getEnvDuration("TIME_TIMEOUT_MS", 100) 

	for {
		task, err := getTask()
		time.Sleep(timeout)
		if err != nil {
			continue
		}

		result, err := performOperation(task)
		
		if err != nil {
			log.Printf("деление на 0")
			continue
		}

		err = postResult(&Result{ID: task.ID, Result: result})
		if err != nil {
		} else {
			log.Printf("Решена задача %s: %f %s %f = %f\n", task.ID, task.Arg1, task.Operation, task.Arg2, result)
		}
	}
}

func Runagent() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	computingPower, err := strconv.Atoi(os.Getenv("COMPUTING_POWER"))
	if err != nil {
		log.Println("Ошибка чтения переменной окружения COMPUTING_POWER. Используется 1 клиент по умолчанию.")
		computingPower = 1
	}

	log.Printf("Запуск %d клиентов...\n", computingPower)

	var wg sync.WaitGroup
	for i := 0; i < computingPower; i++ {
		wg.Add(1)
		go clientWorker(&wg)
	}

	wg.Wait()
	log.Println("Все клиенты завершили работу.")
}
