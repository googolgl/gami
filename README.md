GAMI
====

GO - Asterisk AMI Interface

The library allow connect to Asterisk AMI and send Actions and
parse Events.

Example connecting to Asterisk and Send Action

```go
package main
import (
	"github.com/bit4bit/GAMI"
)

func main() {
	gami, err := gami.Dial("127.0.0.1:5038")
	if err != nil {
		fmt.Print(err)
		os.Exit(1)
	}
	
	go func() {
		for {
			select {
			//wait events and process
			case ev := <-gami.Events:
				fmt.Printf("Event Detect: %v", ev)
			}
		}
	}()
	
	if err := gami.Login("admin", "root"); err != nil {
		fmt.Print(err)
	}
	
	
	if rs, err := gami.Action("Ping", nil); err != nil {
		fmt.Print(rs)
	}
	if rs, err := gami.Action("Events", map[string]string{"EventMask":"on"}); err != nil {
		fmt.Print(err)
	}
	
	gami.Close()
}
```