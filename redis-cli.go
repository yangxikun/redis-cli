package main

import (
	"flag"
	"fmt"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/holys/goredis"
	"github.com/peterh/liner"
)

var (
	hostname    = flag.String("h", "127.0.0.1", "Server hostname")
	port        = flag.Int("p", 6379, "Server server port")
	socket      = flag.String("s", "", "Server socket. (overwrites hostname and port)")
	dbn         = flag.Int("n", 0, "Database number(default 0)")
	auth        = flag.String("a", "", "Password to use when connecting to the server")
	outputRaw   = flag.Bool("raw", false, "Use raw formatting for replies")
	showWelcome = flag.Bool("welcome", false, "show welcome message, mainly for web usage via gotty")
)

var (
	line        *liner.State
	historyPath = path.Join(os.Getenv("HOME"), ".gorediscli_history") // $HOME/.gorediscli_history

	mode int

	client *goredis.Client
)

//output
const (
	stdMode = iota
	rawMode
)

func main() {
	flag.Parse()

	if *outputRaw {
		mode = rawMode
	} else {
		mode = stdMode
	}

	// Start interactive mode when no command is provided
	if flag.NArg() == 0 {
		repl()
	}

	noninteractive(flag.Args())
}

// Read-Eval-Print Loop
func repl() {
	line = liner.NewLiner()
	defer line.Close()
	line.SetCtrlCAborts(true)

	setCompletionHandler()
	loadHistory()
	defer saveHistory()

	reg, _ := regexp.Compile(`'.*?'|".*?"|\S+`)
	prompt := ""

	cliConnect()

	if *showWelcome {
		showWelcomeMsg()
	}

	for {
		addr := addr()
		if *dbn > 0 && *dbn < 16 {
			prompt = fmt.Sprintf("%s[%d]> ", addr, *dbn)
		} else {
			prompt = fmt.Sprintf("%s> ", addr)
		}

		cmd, err := line.Prompt(prompt)
		if err != nil {
			fmt.Printf("%s\n", err.Error())
			return
		}

		cmds := reg.FindAllString(cmd, -1)
		if len(cmds) == 0 {
			continue
		} else {
			appendHistory(cmds)

			cmd := strings.ToLower(cmds[0])
			if cmd == "help" || cmd == "?" {
				printHelp(cmds)
			} else if cmd == "quit" || cmd == "exit" {
				os.Exit(0)
			} else if cmd == "clear" {
				println("Please use Ctrl + L instead")
			} else if cmd == "connect" {
				reconnect(cmds[1:])
			} else if cmd == "mode" {
				switchMode(cmds[1:])
			} else if cmd[0] == '-' {
				externalCmd(cmds)
			} else {
				cliSendCommand(cmds)
			}
		}
	}
}

func appendHistory(cmds []string) {
	// make a copy of cmds
	cloneCmds := make([]string, len(cmds))
	for i, cmd := range cmds {
		cloneCmds[i] = cmd
	}

	// for security reason, hide the password with ******
	if len(cloneCmds) == 2 && strings.ToLower(cloneCmds[0]) == "auth" {
		cloneCmds[1] = "******"
	}
	if len(cloneCmds) == 4 && strings.ToLower(cloneCmds[0]) == "connect" {
		cloneCmds[3] = "******"
	}
	line.AppendHistory(strings.Join(cloneCmds, " "))
}

func cliSendCommand(cmds []string) {
	cliConnect()

	if len(cmds) == 0 {
		return
	}

	args := make([]interface{}, len(cmds[1:]))
	for i := range args {
		args[i] = strings.Trim(string(cmds[1+i]), "\"'")
	}

	cmd := strings.ToLower(cmds[0])

	if cmd == "monitor" {
		respChan := make(chan interface{})
		stopChan := make(chan struct{})
		err := client.Monitor(respChan, stopChan)
		if err != nil {
			fmt.Printf("(error) %s\n", err.Error())
			return
		}
		for {
			select {
			case mr := <-respChan:
				printReply(0, mr, mode)
				fmt.Printf("\n")
			case <-stopChan:
				fmt.Println("Error: Server closed the connection")
				return
			}
		}

	}

	r, err := client.Do(cmd, args...)
	if err == nil && strings.ToLower(cmd) == "select" {
		*dbn, _ = strconv.Atoi(cmds[1])
	}
	if err != nil {
		fmt.Printf("(error) %s", err.Error())
	} else {
		if cmd == "info" {
			printInfo(r)
		} else {
			printReply(0, r, mode)
		}
	}

	fmt.Printf("\n")
}

func cliConnect() {
	if client == nil {
		addr := addr()
		client = goredis.NewClient(addr, "")
		client.SetMaxIdleConns(1)
		sendPing(client)
		sendSelect(client, *dbn)
		sendAuth(client, *auth)
	}
}

func reconnect(args []string) {
	if len(args) < 2 {
		fmt.Println("(error) invalid connect arguments. At least provides host and port.")
		return
	}

	h := args[0]
	p := args[1]

	var auth string
	if len(args) > 2 {
		auth = args[2]
	}

	if h != "" && p != "" {
		addr := fmt.Sprintf("%s:%s", h, p)
		client = goredis.NewClient(addr, "")
	}

	if err := sendPing(client); err != nil {
		return
	}

	// change prompt
	hostname = &h
	intp, _ := strconv.Atoi(p)
	port = &intp

	if auth != "" {
		err := sendAuth(client, auth)
		if err != nil {
			return
		}
	}

	fmt.Printf("connected %s:%s successfully \n", h, p)
}

func switchMode(args []string) {
	if len(args) != 1 {
		fmt.Println("invalid args. Should be MODE [raw|std]")
		return
	}

	m := strings.ToLower(args[0])
	if m != "raw" && m != "std" {
		fmt.Println("invalid args. Should be MODE [raw|std]")
		return
	}

	switch m {
	case "std":
		mode = stdMode
	case "raw":
		mode = rawMode
	}

	return
}

func addr() string {
	var addr string
	if len(*socket) > 0 {
		addr = *socket
	} else {
		addr = fmt.Sprintf("%s:%d", *hostname, *port)
	}
	return addr
}

func noninteractive(args []string) {
	cliSendCommand(args)
}

func printInfo(reply interface{}) {
	switch reply := reply.(type) {
	case []byte:
		fmt.Printf("%s", reply)
	//some redis proxies don't support this command.
	case goredis.Error:
		fmt.Printf("(error) %s", string(reply))
	}
}

func printReply(level int, reply interface{}, mode int) {
	switch mode {
	case stdMode:
		printStdReply(level, reply)
	case rawMode:
		printRawReply(level, reply)
	default:
		printStdReply(level, reply)
	}

}

func printStdReply(level int, reply interface{}) {
	switch reply := reply.(type) {
	case int64:
		fmt.Printf("(integer) %d", reply)
	case string:
		fmt.Printf("%s", reply)
	case []byte:
		fmt.Printf("%q", reply)
	case nil:
		fmt.Printf("(nil)")
	case goredis.Error:
		fmt.Printf("(error) %s", string(reply))
	case []interface{}:
		for i, v := range reply {
			if i != 0 {
				fmt.Printf("%s", strings.Repeat(" ", level*4))
			}

			s := fmt.Sprintf("%d) ", i+1)
			fmt.Printf("%-4s", s)

			printStdReply(level+1, v)
			if i != len(reply)-1 {
				fmt.Printf("\n")
			}
		}
	default:
		fmt.Printf("Unknown reply type: %+v", reply)
	}
}

func printRawReply(level int, reply interface{}) {
	switch reply := reply.(type) {
	case int64:
		fmt.Printf("%d", reply)
	case string:
		fmt.Printf("%s", reply)
	case []byte:
		fmt.Printf("%s", reply)
	case nil:
		// do nothing
	case goredis.Error:
		fmt.Printf("%s\n", string(reply))
	case []interface{}:
		for i, v := range reply {
			if i != 0 {
				fmt.Printf("%s", strings.Repeat(" ", level*4))
			}

			printRawReply(level+1, v)
			if i != len(reply)-1 {
				fmt.Printf("\n")
			}
		}
	default:
		fmt.Printf("Unknown reply type: %+v", reply)
	}
}

func printGenericHelp() {
	msg :=
		`redis-cli
Type:	"help <command>" for help on <command>
	`
	fmt.Println(msg)
}

func printCommandHelp(arr []string) {
	fmt.Println()
	fmt.Printf("\t%s %s \n", arr[0], arr[1])
	fmt.Printf("\tGroup: %s \n", arr[2])
	fmt.Println()
}

func printHelp(cmds []string) {
	args := cmds[1:]
	if len(args) == 0 {
		printGenericHelp()
	} else if len(args) > 1 {
		fmt.Println()
	} else {
		cmd := strings.ToUpper(args[0])
		for i := 0; i < len(helpCommands); i++ {
			if helpCommands[i][0] == cmd {
				printCommandHelp(helpCommands[i])
			}
		}
	}
}

func sendSelect(client *goredis.Client, index int) {
	if index == 0 {
		// do nothing
		return
	}
	if index > 16 || index < 0 {
		index = 0
		fmt.Println("index out of range, should less than 16")
	}
	_, err := client.Do("SELECT", index)
	if err != nil {
		fmt.Printf("%s\n", err.Error())
	}
}

func sendAuth(client *goredis.Client, passwd string) error {
	if passwd == "" {
		// do nothing
		return nil
	}

	resp, err := client.Do("AUTH", passwd)
	if err != nil {
		fmt.Printf("(error) %s\n", err.Error())
		return err
	}

	switch resp := resp.(type) {
	case goredis.Error:
		fmt.Printf("(error) %s\n", resp.Error())
		return resp
	}

	return nil
}

func sendPing(client *goredis.Client) error {
	_, err := client.Do("PING")
	if err != nil {
		fmt.Printf("%s\n", err.Error())
		return err
	}
	return nil
}

func setCompletionHandler() {
	line.SetCompleter(func(line string) (c []string) {
		for _, i := range helpCommands {
			if strings.HasPrefix(i[0], strings.ToUpper(line)) {
				c = append(c, i[0])
			}
		}
		return
	})
}

func loadHistory() {
	if f, err := os.Open(historyPath); err == nil {
		line.ReadHistory(f)
		f.Close()
	}
}

func saveHistory() {
	if f, err := os.Create(historyPath); err != nil {
		fmt.Printf("Error writing history file: %s", err.Error())
	} else {
		line.WriteHistory(f)
		f.Close()
	}
}

func showWelcomeMsg() {
	welcome := `
	Welcome to redis-cli online.
	You can switch to different redis instance with the CONNECT command. 
	Usage: CONNECT host port [auth]

	Switch output mode with MODE command. 

	Usage: MODE [std | raw]
	`
	fmt.Println(welcome)
}

func externalCmd(cmds []string) {
	cliConnect()
	switch cmds[0] {
	case "-PDEL":
		if len(cmds) != 2 {
			fmt.Println("ERR wrong number of arguments for '-pdel' external command")
			return
		}
		var delCount, cursor int64
		args := make([]interface{}, 5)
		for {
			args[0] = cursor
			args[1] = "match"
			args[2] = cmds[1]
			args[3] = "count"
			args[4] = 100
			reply, err := client.Do("scan", args...)
			if err != nil {
				fmt.Printf("(error) %s\n", err.Error())
				return
			}
			switch reply := reply.(type) {
			case []interface{}:
				cursor, err = strconv.ParseInt(string(reply[0].([]byte)), 10, 64)
				if err != nil {
					fmt.Printf("Unknown reply type: %+v\n", reply)
					return
				}
				if len(reply[1].([]interface{})) > 0 {
					count, err := client.Do("del", reply[1].([]interface{})...)
					if err != nil {
						fmt.Printf("(error) %s\n", err.Error())
						return
					}
					delCount += count.(int64)
				}
			case goredis.Error:
				fmt.Printf("(error) %s\n", string(reply))
				return
			default:
				fmt.Printf("Unknown reply type: %+v\n", reply)
				return
			}
			if cursor == 0 {
				break
			}
		}
		fmt.Println(delCount)
	}
}
