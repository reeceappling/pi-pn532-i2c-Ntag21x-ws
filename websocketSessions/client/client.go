package client

import (
	"context"
	"errors"
	"fmt"
	"github.com/clausecker/nfc/v2"
	"github.com/gorilla/websocket"
	"github.com/reeceappling/freefare"
	"github.com/reeceappling/goUtils/v2/utils"
	"github.com/reeceappling/pi-pn532-i2c-Ntag21x-ws/v2/websocketSessions/shared"
	"log"
	"net/http"
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

var (
	ErrHandshakeFailure = errors.New("websocket initial handshake failure")
	ErrClosing          = errors.New("error closing websocket client connection")
	ErrNoDialResponse   = errors.New("nil initial response from opening websocket on client")
	ErrWrongTag         = errors.New("not Ntag21x")
)

// New starts a new client closeable via the context passed in
func New(ctx context.Context, Name, RemoteHost, RemoteEndpoint string, RemotePort int, serviceSecret string, customScheme *string) (context.CancelFunc, error) {
	clientCtx, closeFunc := context.WithCancel(ctx)
	scheme := utils.Default(customScheme, "ws")
	host := fmt.Sprintf(`%s:%d`, RemoteHost, RemotePort) // TODO: ensure 443 ok!
	client := Client{
		name:          Name,
		ServerUrl:     url.URL{Scheme: scheme, Host: host, Path: RemoteEndpoint},
		serviceSecret: serviceSecret,
		close:         closeFunc,
	}
	headers := http.Header{
		"Origin": []string{RemoteHost}, // TODO: trial doing the origin stuff! search "trial doing the origin stuff!"
	}
	println("dialing", client.ServerUrl.String())
	conn, resp, err := websocket.DefaultDialer.Dial(client.ServerUrl.String(), headers) // TODO: non-default dialer?
	if err != nil {
		if resp == nil {
			return nil, errors.Join(
				ErrNoDialResponse, err,
			)
		}
		return nil, errors.Join(
			ErrHandshakeFailure,
			fmt.Errorf("handshake failed with status %d\n", resp.StatusCode),
			err,
		)
	}
	client.conn = conn // TODO: figure out when to close!
	err = client.connectAndListen(clientCtx)
	if err != nil {
		return nil, err
	}
	return closeFunc, nil
}

func (client Client) Close() { // TODO: figure out where to use!
	client.close()
}

func (client Client) signUp(ctx context.Context) (err error) {
	// send signup message
	err = client.sendSignupRequest()
	if err != nil {
		errr := errors.Join(errors.New("failed sendSignupRequest"), err)
		println(errr.Error()) // TODO: del
		return errr
	}

	// Get and validate signup response
	err = client.parseAndValidateSignupResponse(ctx)
	if err != nil {
		errr := errors.Join(errors.New("failed parseAndValidateSignupResponse"), err)
		println(errr.Error()) // TODO: del
		return errr
	}
	return nil
}
func (client Client) sendSignupRequest() (err error) {
	sur := shared.NewSignupRequest(client.name, client.serviceSecret)
	println("created signup request")
	err = sur.WriteTo(client.conn)
	if err != nil {
		println("failed to send signup request")
		return err
	}
	return nil
}
func (client Client) parseAndValidateSignupResponse(ctx context.Context) (err error) {
	println("trying to get response") // TODO: del
	// Get and validate signup response
	received := shared.TryGetMessage(ctx, client.conn, 35*time.Second) // TODO: time ok?
	println("got signup response, validating")
	resp, err := received.AsSignupResponse(client.name) // TODO: flip to client.ValidateSignupResponse(m) and remove resp.Validate(client.name)
	if err != nil {
		println("failed to parse or invalid response message as signup response: " + err.Error())
		return err
	}
	return resp.Validate(client.name)
}

func (client Client) connectAndListen(ctx context.Context) (err error) { // TODO: RETURN VALUES
	defer func() {
		errC := client.conn.Close() // Close connection at the end
		if errC != nil {
			err = errors.Join(ErrClosing, errC) // TODO: ensure this makes it out in tests!
		}
		// TODO: something here
	}()

	err = client.signUp(ctx)
	if err != nil {
		println("failed signup: " + err.Error())
		return err
	}
	// TODO: ensure we won't get colliding messages (handled manager-side)
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
	// TODO: OVERHAUL????
	m := shared.TryGetMessage(ctx, client.conn, 10*time.Second)
	err := m.Err
	if err != nil {
		if errors.Is(err, shared.ErrGetMessageTimeout) { // Don't crash on non-found message
			println("listenAndHandleOne timed out") // TODO: probably dont time out on the initial message...
			return nil
		}
		// TODO: probably no error here?
		return errors.Join(errors.New("failed to read websocket response on client"), m.Err) // TODO: ok that we will crash on this?
	}
	var outgoing = &shared.SocketMessage{}
	switch m.MsgType {
	case websocket.PingMessage: // For keeping session alive
		outgoing = client.handleRenewalRequest(m)
	case websocket.TextMessage:
		outgoing = shared.NewErrorResponse(errors.New("reader got an error (text) message from server, should never happen: " + string(m.Bytes)))
	case websocket.BinaryMessage:
		if m.Bytes == nil {
			outgoing = shared.NewErrorResponse(errors.New("reader/writer got a nil binary message, should never happen"))
			break
		}
		if len(m.Bytes) == 0 {
			outgoing = shared.NewErrorResponse(errors.New("reader/writer got an empty binary message, should never happen"))
			break
		}
		switch m.Bytes[0] {
		case shared.FirstByteRead:
			outgoing = client.handleReadRequest(m)
		case shared.FirstByteWrite:
			outgoing = client.handleWriteRequest(m)
		default:
			outgoing = shared.NewErrorResponse(fmt.Errorf("invalid binary message first byte %d", m.Bytes[0]))
		}

	case websocket.CloseMessage: // For closing client
		// TODO: close the websocket/restart!
		return errors.New("closing websocket gracefully")
	default:
		str := ""
		if m.Bytes != nil && len(m.Bytes) > 0 {
			str = string(m.Bytes)
		}
		outgoing = shared.NewErrorResponse(fmt.Errorf(`unsupported websocket messageType %d and contents: %s`, m.MsgType, str))
	}

	err = outgoing.WriteTo(client.conn)
	if err != nil {
		println("write output failed for reason: " + err.Error()) // TODO: ok to not crash on this?
	}
	return nil
}

func (client Client) handleReadRequest(m shared.ReceivedMsg) (response *shared.SocketMessage) {
	err := m.ValidateReadRequest()
	if err != nil {
		return shared.NewErrorResponse(err)
	}
	tempResp, err := readUserData()
	if err != nil {
		return shared.NewErrorResponse(err)
	}
	return shared.NewReadResponse(tempResp)
}
func (client Client) handleRenewalRequest(m shared.ReceivedMsg) (response *shared.SocketMessage) {
	err := m.ValidateRenewalRequest(client.name)
	if err != nil {
		return shared.NewErrorResponse(err)
	}
	return shared.NewRenewalResponse(client.serviceSecret)
}
func (client Client) handleWriteRequest(m shared.ReceivedMsg) (response *shared.SocketMessage) {
	toWrite, err := m.ValidateWriteRequest()
	if err != nil {
		return shared.NewErrorResponse(err)
	}
	err = writeUserData(toWrite)
	if err != nil { // TODO: validate wrote correctly?
		return shared.NewErrorResponse(errors.Join(errors.New("failed to write tag data"), err))
	}
	return shared.NewWriteResponse(toWrite)
}

func OpenDevice() (nfc.Device, error) {
	device, err := nfc.Open("pn532_i2c:/dev/i2c-1")
	if err != nil {
		return device, errors.Join(errors.New("failed to open device"), err)
	}
	return device, nil
}

func readUserData() (out [shared.RfidByteSize]byte, err error) {
	device, err := OpenDevice()
	if err != nil {
		return out, err
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
		return out, ErrWrongTag
	}
	return readUserDataInternal(tag.(freefare.UltralightTag))
}

func writeUserData(newUID [shared.RfidByteSize]byte) (err error) {
	device, err := OpenDevice()
	if err != nil {
		return err
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
		return ErrWrongTag
	}
	return writeUserDataInternal(tag.(freefare.UltralightTag), newUID) // TODO: ENSURE WRITING CORRECT SIZE!
}

func readUserDataInternal(ntag freefare.UltralightTag) ([shared.RfidByteSize]byte, error) {
	println("reading user data") // TODO: del
	UID := [8]byte{}
	for i := 0; i < 2; i++ { // 2 pages of 4 bytes each
		userData, err := ntag.ReadPage(uint8(i + 4)) // TODO: why +4???? (is it because there are unwriteable or settings bytes in the first 4?)
		if err != nil {
			return UID, errors.Join(err, fmt.Errorf("failed to read user data for page %d", i))
		}
		for j, dataByte := range userData {
			UID[(i*4)+j] = dataByte
		}
	}
	return UID, nil
}

func writeUserDataInternal(ntag freefare.UltralightTag, newUID [shared.RfidByteSize]byte) error {
	// TODO: PROBABLY OVERHAUL!
	initialUID, err := readUserDataInternal(ntag) // TODO: probably dont need
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
		// TODO: maybe dont error but just print a warning like so!
		println("warning: failed to read user data to confirm write")
		return nil
	}
	// Reread to confirm (optional)
	finalStr := string(finalUID[:])
	if finalStr != string(newUID[:]) {
		return fmt.Errorf("Mismatch of written values!\nWas:\n%s\nShould be:\n%s\n", finalStr, string(newUID[:]))
	}
	return nil
}
