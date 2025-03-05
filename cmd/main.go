package main
import(
	"github.com/IvanSolomatin/calc_v2/internal/app"
)

func main(){
	go func(){
		app.Runagent()
	}()
	
	app.Runorchestrator()
	
}