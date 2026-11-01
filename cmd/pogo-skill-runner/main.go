// This is a short-lived child of Pogo, inside bubblewrap, never a service.
package main

import (
	"os"

	"github.com/yosukeno/pogo/skillruntime"
)

func main() { os.Exit(skillruntime.RunLocalRunner(os.Args[1:])) }
