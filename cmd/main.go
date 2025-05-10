package main
import(
	"github.com/IvanSolomatin/calc_v2/internal/app"
)

func main(){
	go func(){
		app.RunAgentGRPC()
	}()
	
	app.RunOrchestrator()
	
}