package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/clausecker/nfc/v2"
	"github.com/gorilla/websocket"
	"github.com/reeceappling/freefare"
	"github.com/reeceappling/pi-pn532-i2c-Ntag21x-ws/v2/websocketSessions"
	"github.com/reeceappling/pi-pn532-i2c-Ntag21x-ws/v2/websocketSessions/client"
	"github.com/reeceappling/pi-pn532-i2c-Ntag21x-ws/v2/websocketSessions/shared"
	"log"
	"net/url"
	"os"
	"time"
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
	// TODO: setup reader/writer
	// Get clientName and signupSecret
	secret := os.Getenv("RFID_SECRET")
	if secret == "" {
		panic("RFID_SECRET env var nonexistent")
	}
	bs, err := os.ReadFile("/config/rfidName.txt")
	if err != nil {
		panic("failed to read nameFile!")
	}
	clientName := string(bs)
	ctx := context.Background()

	err, closeClient := client.New(ctx, clientName, serverHostname, "/ws", 443, secret, nil)
	if err != nil {
	}
	defer closeClient()

	// TODO: do we want to setup websocket repeatedly?
	websocketServerUrl := url.URL{Scheme: "ws", Host: serverHostname, Path: "/ws"} // TODO: ENSURE websocket encrypted?????? PORT???
	c, resp, errDial := websocket.DefaultDialer.Dial(websocketServerUrl.String(), nil)
	if errDial != nil {
		if resp == nil {
			ErrNoDialResponse := errors.New("nil initial response from opening websocket on client") // TODO: MOVE
			err = errors.Join(ErrNoDialResponse, errDial)
			panic(err.Error())
		}
		ErrHandshakeFailure := errors.New("websocket initial handshake failure") // TODO: MOVE
		specificErr := fmt.Errorf("handshake failed with status %d\n", resp.StatusCode)
		err = errors.Join(ErrHandshakeFailure, specificErr)
		panic(err.Error())
	}
	defer func() {
		ErrClosing := errors.New("error closing websocket client connection")
		err = c.Close() // Close connection at the end
		if err != nil {
			err = errors.Join(ErrClosing, err) // TODO: ensure this makes it out in tests!
			println(err.Error())
		}
	}()

	//err = client.ClientSignup(c, clientName, secret) // TODO: ?????
	//if err != nil {
	//	panic("failed client signup: " + err.Error())
	//}

	// TODO: ensure we won't get colliding messages
	// Start listening for real messages
	for {
		ctxTimeout, cancel := context.WithTimeout(ctx, 10*time.Second) // TODO: time ok?
		m := shared.TryGetMessage(ctxTimeout, c)
		cancel()
		if m.Err != nil {
			if errors.Is(err, shared.ErrGetMessageTimeout) { // Don't crash on non-found message
				continue
			}
			fmt.Println("Error reading from websocket on client:", m.Err)
			panic("NO RESPONSE") // TODO: WHAT HERE????
		}
		var outgoing *shared.SocketMessage
		switch m.MsgType {
		case websocket.PingMessage: // For keeping session alive
			err = m.ValidateRenewalRequest(clientName)
			if err != nil {
				panic("NO RESPONSE") // TODO: WHAT HERE????
			}
			outgoing = shared.NewRenewalResponse(secret)
			err = outgoing.WriteTo(c) // TODO: move
			if err != nil {
				fmt.Println("Error writing ping to websocket from client:", err) // TODO: handle?
			}
			continue
		case websocket.CloseMessage: // For closing client
			println("closing websocket") // TODO: ok?
			return
		case websocket.BinaryMessage:
			if len(m.Bytes) == 0 {
				panic("EMPTY BINARY MESSAGE")
			}
			switch m.Bytes[0] {
			case shared.FirstByteRead:
				if err = m.ValidateReadRequest(); err != nil {
					panic("INVALID READ REQUEST") // TODO: CHANGE
				}
				rres, err := readUserData()
				if err != nil {
					panic("INVALID READ REQUEST") // TODO: CHANGE
				}
				outgoing = shared.NewReadResponse(rres)
				err = outgoing.WriteTo(c) // TODO MOVE
				if err != nil {
					panic("INVALID READ REQUEST") // TODO: CHANGE
				}

			case shared.FirstByteWrite:
				toWrite, err := m.ValidateWriteRequest()
				if err != nil {
					panic("INVALID READ REQUEST") // TODO: CHANGE
				}
				err = writeUserData(toWrite)
				if err != nil {
					panic("INVALID READ REQUEST") // TODO: CHANGE
				}
				outgoing = shared.NewWriteRequest(toWrite)
				err = outgoing.WriteTo(c) // TODO: MOVE
				if err != nil {
					panic("INVALID READ REQUEST") // TODO: CHANGE
				}
			default:
				panic("INVALID BINARY MESSAGE")
				// TODO: ERROR!!!!!
			}

			var outgoing *shared.SocketMessage
			incoming := shared.SocketMessage{}
			if err = json.Unmarshal(msgBytes, &incoming); err != nil {
				err = outgoing.WithType(shared.MessageTypeError).WithData([]byte(err.Error())).WriteTo(c)
				if err != nil {
					fmt.Println("Error writing error to websocket for binary msg:", err.Error()) // TODO: handle?
				}
				// TODO: handle error?
				continue
			}
			switch incoming.Type {
			case shared.MessageTypeWrite: // TODO: ONLY ACCEPTS BASE 2!!!!!
				toWrite := string(incoming.Data)
				if len(incoming.Data) != 8 {
					outgoing.WithType(shared.MessageTypeError).WithData([]byte("invalid incoming data size")) // TODO: size?
				} else {
					// Try to write the data
					if err = writeUserData([8]byte(incoming.Data)); err != nil {
						outgoing.WithType(shared.MessageTypeError).WithData([]byte("failed to write user data: " + err.Error()))
					} else {
						outgoing.WithType(shared.MessageTypeWrite).WithData([]byte(toWrite))
					}
				}

				// Respond
				if err = outgoing.WriteTo(c); err != nil {
					fmt.Println("Error writing binary response to websocket from client:", err)
				}
			default:
				// do nothing, let it time out
				fmt.Printf("unsupported Binary message type, %d", incoming.Type)
			}

		case websocket.TextMessage:
			msgString := string(msgBytes)
			switch msgString {
			case websocketSessions.ReadEndpt: // TODO: ONLY OUTPUTS BASE 2!!!!
				readResponse, err := readUserData() // Read tag data
				if err != nil {
					fmt.Println("failed to get read response", err) // TODO: retry?
				}
				if err = c.WriteMessage(websocket.TextMessage, readResponse[:]); err != nil {
					fmt.Println("Error writing to websocket for read message on client:", err)
				}
				continue
			default:
				fmt.Printf(`Unsupported text message: %s`, msgString)
			}
		default:
			fmt.Printf(`Unsupported websocket messageType: %d`, msgType)
		}
	}
}

func readUserData() (out [8]byte, err error) {
	device, err := nfc.Open("pn532_i2c:/dev/i2c-1") // TODO: get device globally????
	if err != nil {
		return out, errors.Join(errors.New("failed to open device"), err)
	}
	defer device.Close()
	tags, err := freefare.GetTags(device)
	if err != nil {
		return out, errors.Join(errors.New("failed to get tags"), err)
	}
	if len(tags) != 1 {
		return out, fmt.Errorf("expected 1 tags, got %d", len(tags))
	}
	tag := tags[0]
	if err = tag.Connect(); err != nil {
		return out, errors.Join(errors.New("failed to connect"), err)
	}
	if tag.Type() != freefare.Ultralight { // TODO: should really be NTAG213 (issue with libNfc and libFreefare), but Ultralight will work for our use case
		return out, errors.New("not Ntag21x") // TODO: fix
	}
	return readUserDataInternal(tag.(freefare.UltralightTag))
}

func writeUserData(newUID [8]byte) (err error) {
	device, err := nfc.Open("pn532_i2c:/dev/i2c-1") // TODO: get device globally????
	if err != nil {
		return errors.Join(errors.New("failed to open device"), err)
	}
	defer device.Close()
	tags, err := freefare.GetTags(device)
	if err != nil {
		return errors.Join(errors.New("failed to get tags"), err)
	}
	if len(tags) != 1 {
		return fmt.Errorf("expected 1 tags, got %d", len(tags))
	}
	tag := tags[0]
	if err = tag.Connect(); err != nil {
		return errors.Join(errors.New("failed to connect"), err)
	}
	if tag.Type() != freefare.Ultralight { // TODO: should really be NTAG213 (issue with libNfc and libFreefare), but Ultralight will work for our use case
		return errors.New("not Ntag21x") // TODO: fix
	}
	return writeUserDataInternal(tag.(freefare.UltralightTag), newUID) // TODO: ENSURE WRITING CORRECT SIZE!
}

func readUserDataInternal(ntag freefare.UltralightTag) ([8]byte, error) {
	// println("reading user data")
	UID := [8]byte{}
	for i := 0; i <= 1; i++ {
		userData, err := ntag.ReadPage(uint8(i + 4))
		if err != nil {
			return UID, errors.Join(err, fmt.Errorf("failed to read user data for page %d", i))
		}
		for j, dataByte := range userData {
			UID[(i*4)+j] = dataByte
		}
	}
	return UID, nil
}

func writeUserDataInternal(ntag freefare.UltralightTag, newUID [8]byte) error {
	initialUID, err := readUserDataInternal(ntag)
	if err != nil {
		log.Fatal(err.Error())
		return err
	}
	write := func(toWriteBytes [8]byte) error {
		for i := 0; i <= 1; i++ {
			page := 4 + i
			err = ntag.WritePage(byte(page), [4]byte(toWriteBytes[i*4:((i+1)*4)]))
			if err != nil {
				return errors.Join(fmt.Errorf("failed to write data for page %d", page), err)
			}
		}
		return nil
	}
	err = write(newUID)
	if err != nil {
		errB := write(initialUID)
		if errB != nil {
			err = errors.Join(errors.New("FAILED TO REWRITE ORIGINAL DATA"), err)
		}
		return err
	}

	//read again to confirm
	finalUID, err := readUserDataInternal(ntag)
	if err != nil {
		return err
	}
	finalStr := string(finalUID[:])
	if finalStr != string(newUID[:]) {
		return fmt.Errorf("Mismatch of written values!\nWas:\n%s\nShould be:\n%s\n", finalStr, string(newUID[:]))
	}
	return nil
}
