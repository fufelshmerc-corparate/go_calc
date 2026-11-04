package app

import (
	"context"
	"errors"
	"fmt"
	pb "github.com/fufelshmerc-corparate/go_calc/api/calc"
	"go/ast"
	"go/parser"
	"go/token"
	"log"
	"math"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

func normalizeNumbers(input string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(input); {
		c := input[i]
		if strings.ContainsRune(" \t\r\n\v\f", rune(c)) {
			i++
			continue
		}
		if c >= '0' && c <= '9' || c == '.' {
			start := i
			for i < len(input) && (input[i] >= '0' && input[i] <= '9' || input[i] == '.') {
				i++
			}
			if i < len(input) && (input[i] == 'e' || input[i] == 'E') {
				i++
				if i < len(input) && (input[i] == '+' || input[i] == '-') {
					i++
				}
				for i < len(input) && input[i] >= '0' && input[i] <= '9' {
					i++
				}
			}
			value, err := strconv.ParseFloat(input[start:i], 64)
			if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
				return "", errors.New("invalid decimal number")
			}
			out.WriteString(strconv.FormatFloat(value, 'g', -1, 64))
			out.WriteByte(' ')
			continue
		}
		if !strings.ContainsRune("+-*/()", rune(c)) {
			return "", errors.New("unsupported character")
		}
		out.WriteByte(c)
		out.WriteByte(' ')
		i++
	}
	return out.String(), nil
}

func parseExpression(input string) (ast.Expr, error) {
	if len(input) == 0 || len(input) > 4096 {
		return nil, errors.New("expression must contain 1..4096 bytes")
	}
	normalized, err := normalizeNumbers(input)
	if err != nil {
		return nil, err
	}
	expr, err := parser.ParseExpr(normalized)
	if err != nil {
		return nil, errors.New("invalid arithmetic expression")
	}
	nodes := 0
	var validate func(ast.Expr, int) error
	validate = func(expr ast.Expr, depth int) error {
		nodes++
		if depth > 64 || nodes > 512 {
			return errors.New("expression too complex")
		}
		switch e := expr.(type) {
		case *ast.BasicLit:
			if e.Kind != token.INT && e.Kind != token.FLOAT {
				return errors.New("only decimal numbers are supported")
			}
			for _, c := range e.Value {
				if !strings.ContainsRune("0123456789.eE+-", c) {
					return errors.New("only decimal numbers are supported")
				}
			}
			v, err := strconv.ParseFloat(e.Value, 64)
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
				return errors.New("invalid number")
			}
		case *ast.ParenExpr:
			return validate(e.X, depth+1)
		case *ast.UnaryExpr:
			if e.Op != token.ADD && e.Op != token.SUB {
				return errors.New("unsupported unary operator")
			}
			return validate(e.X, depth+1)
		case *ast.BinaryExpr:
			if e.Op != token.ADD && e.Op != token.SUB && e.Op != token.MUL && e.Op != token.QUO {
				return errors.New("unsupported operator")
			}
			if err := validate(e.X, depth+1); err != nil {
				return err
			}
			return validate(e.Y, depth+1)
		default:
			return errors.New("unsupported expression")
		}
		return nil
	}
	if err := validate(expr, 0); err != nil {
		return nil, err
	}
	return expr, nil
}

func (o *Orchestrator) processExpression(expr ast.Expr, taskID string) {
	defer o.jobs.Done()
	defer func() { <-o.slots }()
	ctx, cancel := context.WithTimeout(o.ctx, 30*time.Second)
	defer cancel()
	result, err := o.eval(ctx, expr)
	state := "completed"
	var value interface{} = result
	if err != nil {
		state = "error"
		value = nil
		log.Printf("expression %s: %v", taskID, err)
	}
	if _, err := o.db.Exec("UPDATE expressions SET status = ?, result = ? WHERE id = ?", state, value, taskID); err != nil {
		log.Printf("save expression %s: %v", taskID, err)
	}
}

func (o *Orchestrator) eval(ctx context.Context, expr ast.Expr) (float64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	switch e := expr.(type) {
	case *ast.BasicLit:
		return strconv.ParseFloat(e.Value, 64)
	case *ast.ParenExpr:
		return o.eval(ctx, e.X)
	case *ast.UnaryExpr:
		v, err := o.eval(ctx, e.X)
		if e.Op == token.SUB {
			v = -v
		}
		return v, err
	case *ast.BinaryExpr:
		left, err := o.eval(ctx, e.X)
		if err != nil {
			return 0, err
		}
		right, err := o.eval(ctx, e.Y)
		if err != nil {
			return 0, err
		}

		task := &pb.Task{Id: fmt.Sprintf("task_%d", atomic.AddUint64(&o.taskCounter, 1)), Arg1: left, Arg2: right, Operation: e.Op.String()}
		o.mu.Lock()
		task.OperationTime = int32(o.opTimes[task.Operation])
		o.mu.Unlock()
		o.addTask(task)
		return o.waitForResult(ctx, task.Id)
	}
	return 0, errors.New("unsupported expression")
}
