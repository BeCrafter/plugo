package main

import (
	"fmt"
	"time"

	"github.com/BeCrafter/plugo"
)

func runPlugin(proto, path string) {
	defer plugo.CostOfFunc()()

	p := plugo.NewPlugin(proto, path)
	// if p.GetStatus() != plugo.StatusRunning {
	// 	p.SetHealthCheck(plugo.HealthConfig{
	// 		Interval:    100 * time.Millisecond,
	// 		MaxRetries:  3,
	// 		RetryDelay:  300 * time.Millisecond,
	// 		AutoRestart: false,
	// 	})
	p.Start()
	// }

	// defer p.Stop()

	objs, err := p.Objects()
	if err != nil {
		fmt.Println(err)
		return
	}

	fmt.Printf("Objects: %s\n", objs)

	var resp string

	if err := p.Call("Plugin.SayHello", "from your plugin", &resp); err != nil {
		fmt.Println("Send err: ", err)
	} else {
		fmt.Printf("Res: %s\n", resp)
	}
	if err := p.Call("Plugin.SayHello", "from your plugin, second call", &resp); err != nil {
		fmt.Println("Send err: ", err)
	} else {
		fmt.Printf("Res2: %s\n", resp)
	}
}

func main() {
	// for i := 0; i < 10; i++ {
	// protocols := []string{"unix", "tcp"}
	protocols := []string{"tcp"}
	for _, p := range protocols {
		fmt.Printf("\nRunning hello world plugin via %s\n", p)

		runPlugin(p, "bin/plugins/plugo-hello-world")

		fmt.Printf("Plugin terminated.\n\n")
	}
	time.Sleep(5 * time.Second)
	// }

	// fmt.Println("Running plugin that fails to register in time")

	// runPlugin("tcp", "bin/plugins/plugo-sleep")

	// fmt.Printf("Plugin terminated.\n\n")
}
