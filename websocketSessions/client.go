package websocketSessions

import (
	"context"
	"errors"
	"fmt"
	"github.com/clausecker/nfc/v2"
	"github.com/gorilla/websocket"
	"github.com/reeceappling/freefare"
	"github.com/reeceappling/goUtils/v2/utils"
	"log"
	"net/url"
	"time"
)

type Client struct {
	name          string
	ServerUrl     url.URL
	serviceSecret string
	conn          *websocket.Conn
	close         context.CancelFunc
}

// NewClient starts a new client closeable via the context passed in
func NewClient(ctx context.Context, Name, RemoteHost, RemoteEndpoint string, RemotePort int, serviceSecret string, customScheme *string) error {
	clientCtx, closeFunc := context.WithCancel(ctx)
	scheme := utils.Default(customScheme, "ws")
	host := fmt.Sprintf(`%s:%d`, RemoteHost, RemotePort) // TODO: ensure 443 ok!
	client := Client{
		name:          Name,
		ServerUrl:     url.URL{Scheme: scheme, Host: host, Path: RemoteEndpoint},
		serviceSecret: serviceSecret,
		close:         closeFunc,
	}
	conn, resp, err := websocket.DefaultDialer.Dial(client.ServerUrl.String(), nil) // TODO: non-default dialer?
	if err != nil {
		if resp == nil {
			ErrNoDialResponse := errors.New("nil initial response from opening websocket on client") // TODO: MOVE
			return errors.Join(ErrNoDialResponse, err)
		}
		ErrHandshakeFailure := errors.New("websocket initial handshake failure") // TODO: MOVE
		specificErr := fmt.Errorf("handshake failed with status %d\n", resp.StatusCode)
		return errors.Join(ErrHandshakeFailure, specificErr)
	}
	client.conn = conn
	return client.connectAndListen(clientCtx)
}

func (client Client) Close() {
	client.close()
}

func (client Client) signUp(ctx context.Context) (err error) {
	// send signup message
	err = NewSignupRequest(client.name, client.serviceSecret).WriteTo(client.conn)
	if err != nil {
		return err
	}
	ctxTimedOut, cancel := context.WithTimeout(ctx, 5*time.Second) // TODO: ensure timeout ok
	defer cancel()
	return TryGetMessage(ctxTimedOut, client.conn).ValidateSignupResponse(client.name)
}

func (client Client) connectAndListen(ctx context.Context) (err error) { // TODO: RETURN VALUES
	defer func() {
		ErrClosing := errors.New("error closing websocket client connection")
		errC := client.conn.Close() // Close connection at the end
		if errC != nil {
			err = errors.Join(ErrClosing, errC) // TODO: ensure this makes it out in tests!
		}
		// TODO: something here
	}()

	err = client.signUp(ctx)
	if err != nil {
		return
	}
	// TODO: ensure we won't get colliding messages
	// Start listening for real messages
	for {
		select {
		case <-ctx.Done():
			return err
		default:
			err = client.listenAndHandleOne(ctx)
			if err != nil {
				println("fatal error handling request, stopping client")
				return
			}
		}
	}
}

func (client Client) listenAndHandleOne(ctx context.Context) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second) // TODO: time ok?
	m := TryGetMessage(timeoutCtx, client.conn)
	cancel()
	err := m.Err
	if err != nil {
		if errors.Is(err, ErrGetMessageTimeout) { // Don't crash on non-found message
			return nil
		}
		return errors.Join(errors.New("failed to read websocket response on client"), m.Err) // TODO: ok that we will crash on this?
	}
	var outgoing = &SocketMessage{}
	switch m.MsgType {
	case websocket.PingMessage: // For keeping session alive
		err = m.ValidateRenewalRequest(client.name)
		if err != nil {
			outgoing = NewErrorResponse(err)
			break
		}
		outgoing = NewRenewalResponse(client.serviceSecret)

	case websocket.TextMessage:
		outgoing = NewErrorResponse(errors.New("reader got an error (text) message from server, should never happen: " + string(m.Bytes)))
	case websocket.BinaryMessage:
		if len(m.Bytes) == 0 {
			outgoing = NewErrorResponse(errors.New("reader/writer got an empty binary message, should never happen"))
			break
		}
		tempResp := [RfidByteSize]byte{}
		switch m.Bytes[0] {
		case FirstByteRead:
			if err = m.ValidateReadRequest(); err != nil {
				outgoing = NewErrorResponse(err)
				break
			}
			tempResp, err = readUserData()
			if err != nil {
				outgoing = NewErrorResponse(err)
				break
			}
			outgoing = NewReadResponse(tempResp)

		case FirstByteWrite:
			tempResp, err = m.ValidateWriteRequest()
			if err != nil {
				outgoing = NewErrorResponse(err)
				break
			}
			err = writeUserData(tempResp)
			if err != nil {
				outgoing = NewErrorResponse(errors.Join(errors.New("failed to write tag data"), err))
				break
			}
			outgoing = NewWriteRequest(tempResp)
		default:
			outgoing = NewErrorResponse(errors.New("invalid binary message first byte"))
		}

	case websocket.CloseMessage: // For closing client
		return errors.New("closing websocket gracefully")
	default:
		str := ""
		if m.Bytes != nil && len(m.Bytes) > 0 {
			str = string(m.Bytes)
		}
		outgoing = NewErrorResponse(fmt.Errorf(`unsupported websocket messageType %d and contents: %s`, m.MsgType, str))
	}

	err = outgoing.WriteTo(client.conn)
	if err != nil {
		println("write output failed for reason: " + err.Error()) // TODO: ok to not crash on this?
	}
	return nil
}

func readUserData() (out [RfidByteSize]byte, err error) {
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

func writeUserData(newUID [RfidByteSize]byte) (err error) {
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

func responseForWrite(newUID [RfidByteSize]byte) (err error) {
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

func readUserDataInternal(ntag freefare.UltralightTag) ([RfidByteSize]byte, error) {
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

func writeUserDataInternal(ntag freefare.UltralightTag, newUID [RfidByteSize]byte) error {
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
