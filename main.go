package main

import (
	"context"
	"github.com/reeceappling/goUtils/v2/utils"
	"github.com/reeceappling/pi-pn532-i2c-Ntag21x-ws/v2/websocketSessions/client"
	"os"
)

func main() {
	serverHostname := os.Getenv("SERVER_HOSTNAME")
	if serverHostname == "" {
		panic("SERVER_HOSTNAME env var nonexistent")
	}
	namespace := os.Getenv("THIS_NAMESPACE")
	if namespace == "" {
		panic("THIS_NAMESPACE env var nonexistent")
	}
	// Get clientName and signupSecret
	secret := os.Getenv("RFID_SECRET")
	if secret == "" {
		panic("RFID_SECRET env var nonexistent")
	}
	//bs, err := os.ReadFile("/config/rfidName.txt")
	//if err != nil {
	//	panic("failed to read nameFile!")
	//}
	clientName := string(namespace)
	ctx := context.Background()
	for {
		println("starting new client connection")
		// There is a panic handler in client.New
		err := client.New(ctx, clientName, serverHostname, "/rfid/ws", 443, secret, utils.Pointer("wss")) // TODO: unsure about https/ws/wss here!
		if err != nil {
			println("Restarting client", "Fatal client error: "+err.Error())
			continue
		}
		println("Client closed gracefully, exiting...")
		return
	}
}
