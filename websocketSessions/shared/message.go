package shared

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gorilla/websocket"
)

const (
	MessageTypeSignup = iota
	MessageTypeError  //1
	MessageTypeWrite
)

var ErrGetMessageTimeout = errors.New("timeout")

type RfidReaderName string

type SocketMessage struct {
	Type int    `json:"type"` // Always binary, ping, pong, or string(error)
	Data []byte `json:"data,omitempty"`
}

func (sockMsg *SocketMessage) WithData(data []byte) *SocketMessage {
	sockMsg.Data = data
	return sockMsg
}
func (sockMsg *SocketMessage) WithType(msgType int) *SocketMessage {
	sockMsg.Type = msgType
	return sockMsg
}
func (sockMsg *SocketMessage) WriteTo(c *websocket.Conn) error {
	respBytes, err := json.Marshal(*sockMsg) // Error should be impossible here
	if err != nil {
		return errors.Join(errors.New("failed to marshal socket message to Bytes"), err)
	}
	err = c.WriteMessage(websocket.BinaryMessage, respBytes)
	if err != nil {
		return errors.Join(errors.New("failed to write socket message to connection"), err)
	}
	return nil
}

const (
	FirstByteSignup uint8 = iota
	FirstByteRead
	FirstByteWrite
	FirstByteRenew
)

type SignupRequest struct {
	ReaderName RfidReaderName
	Secret     string
}

func NewSocketMessage(typ int, data []byte) *SocketMessage {
	return &SocketMessage{
		Type: typ,
		Data: data,
	}
}
func NewErrorResponse(err error) *SocketMessage {
	return NewSocketMessage(websocket.TextMessage /* TODO: is this ok?*/, []byte(err.Error()))
}
func NewReadRequest() *SocketMessage {
	return NewSocketMessage(websocket.BinaryMessage, []byte{FirstByteRead})
}
func NewReadResponse(r [RfidByteSize]byte) *SocketMessage {
	return NewSocketMessage(websocket.BinaryMessage, append([]byte{FirstByteRead}, r[:]...))
}
func NewWriteRequest(w [RfidByteSize]byte) *SocketMessage { // TODO: req/res are the same for this one
	return NewSocketMessage(websocket.BinaryMessage, append([]byte{FirstByteWrite}, w[:]...))
}

func NewSignupRequest(readerName, secret string) *SocketMessage {
	bs, _ := json.Marshal(SignupRequest{
		ReaderName: RfidReaderName(readerName),
		Secret:     secret,
	})
	data := append([]byte{FirstByteSignup}, bs...)
	return NewSocketMessage(websocket.BinaryMessage, data)
}
func NewSignupResponse(clientName RfidReaderName) *SocketMessage { // TODO: do we even need this?
	data := append([]byte{FirstByteSignup}, []byte(clientName)...)
	return NewSocketMessage(websocket.BinaryMessage, data)
}
func NewRenewalRequest(readerName string) *SocketMessage { // TODO: Comes from the server to client???
	data := append([]byte{FirstByteRenew}, []byte(readerName)...)
	return NewSocketMessage(websocket.PingMessage /* TODO: is this ok?*/, data)
}
func NewRenewalResponse(secret string) *SocketMessage { // TODO: USE THIS // TODO: from client to server???
	data := append([]byte{FirstByteRenew}, []byte(secret)...) // TODO: is this ok????
	return NewSocketMessage(websocket.PongMessage /* TODO: is this ok?*/, data)
}
func (res ReceivedMsg) ValidateRenewalRequest(expName string) error {
	return res.genericValidate(websocket.PingMessage, FirstByteRenew, expName, "renewal request")
}

func (res ReceivedMsg) ValidateRenewalResponse(expSecret string) error {
	return res.genericValidate(websocket.PongMessage, FirstByteRenew, expSecret, "renewal response")
}

func (res ReceivedMsg) genericValidate(expMsgType int, expFirstByte uint8, exStr string, what string) error {
	resp, err := res.GetResponseData(expMsgType, expFirstByte)
	if err != nil {
		return err
	}
	if string(resp) != exStr {
		return fmt.Errorf(`received incorrect secret on %s`, what)
	}
	return nil
}

func TryGetMessage(ctx context.Context, conn *websocket.Conn) ReceivedMsg {
	resultChan := make(chan ReceivedMsg, 1)
	go func() {
		defer close(resultChan)
		msgType, bytes, err := conn.ReadMessage()
		resultChan <- ReceivedMsg{msgType, bytes, err}
	}()
	select {
	case res := <-resultChan:
		return res
	case <-ctx.Done():
		return ReceivedMsg{
			MsgType: MessageTypeError,
			Bytes:   nil,
			Err:     ErrGetMessageTimeout,
		}
	}
}

type ReceivedMsg struct {
	MsgType int
	Bytes   []byte
	Err     error
}

func (res ReceivedMsg) ValidateSignupResponse(expName string) error {
	if res.Err != nil {
		return res.Err
	}
	resp, err := res.GetResponseData(websocket.BinaryMessage, FirstByteSignup)
	if err != nil {
		return err
	}
	if string(resp[:]) != expName {
		return errors.New("signup response data does not match requested")
	}
	return nil
}

const RfidByteSize = 8 // TODO: is this correct?? must match RfidByteSize in mdb.go
func (res ReceivedMsg) ValidateWriteRequest() (toWrite [RfidByteSize]byte, err error) {
	if res.Err != nil {
		return [RfidByteSize]byte{}, res.Err
	}
	resp, err := res.GetResponseData(websocket.BinaryMessage, FirstByteWrite)
	if err != nil {
		return [RfidByteSize]byte{}, err
	}
	if len(resp) != RfidByteSize {
		return [RfidByteSize]byte{}, errors.New("invalid write request size")
	}
	return [RfidByteSize]byte(resp), nil
}

func (res ReceivedMsg) ValidateWriteResponse(expectedWritten [RfidByteSize]byte) error {
	resp, err := res.GetResponseData(websocket.BinaryMessage, FirstByteWrite)
	if err != nil {
		return err
	}
	if string(expectedWritten[:]) != string(resp) {
		return errors.New("bad response content, wrote wrong Bytes")
	}
	return nil
}

func (res *ReceivedMsg) ValidateReadRequest() error {
	_, err := res.GetResponseData(websocket.BinaryMessage, FirstByteRead)
	return err
}

func (res ReceivedMsg) ProcessReadResponse() (result [RfidByteSize]byte, err error) {
	resp, err := res.GetResponseData(websocket.BinaryMessage, FirstByteRead)
	if err != nil {
		return result, err
	}
	if len(resp) != RfidByteSize {
		return result, errors.New("bad read response size")
	}
	return [RfidByteSize]byte(resp), nil
}

func (res ReceivedMsg) GetResponseData(expMsgType int, expFirstByte byte) (resultWithoutTypeByte []byte, err error) { // TODO: result WITHOUT type byte?
	resultWithoutTypeByte = nil
	if res.Err != nil {
		err = errors.Join(errors.New("error reading response from websocket on client"), res.Err)
		return
	}
	// validate response is as expected
	resp := &SocketMessage{}
	if err = json.Unmarshal(res.Bytes, resp); err != nil {
		return
	}
	if res.MsgType == websocket.TextMessage {
		// Error message
		err = errors.New(string(resp.Data))
		return
	}
	if res.MsgType != expMsgType {
		err = errors.New("unexpected message format for response")
		return
	}
	if resp.Data[0] != expFirstByte {
		err = fmt.Errorf(`first byte was expected to be %d, got %d`, int(expFirstByte), resp.Data[0])
		return
	}
	if len(resp.Data) == 1 {
		return nil, nil // TODO: ok?
	}
	return resp.Data[1:], nil // TODO: resp.Data[1:]
}
