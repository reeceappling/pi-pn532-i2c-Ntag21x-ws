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
	Name   RfidReaderName
	Secret string
}

func NewErrorResponse(err error) *SocketMessage {
	return &SocketMessage{
		Type: websocket.TextMessage,
		Data: []byte(err.Error()),
	}
}
func NewReadRequest() *SocketMessage {
	return &SocketMessage{
		Type: websocket.BinaryMessage,
		Data: []byte{FirstByteRead},
	}
}
func NewReadResponse(r [RfidByteSize]byte) *SocketMessage {
	return &SocketMessage{
		Type: websocket.BinaryMessage,
		Data: append([]byte{FirstByteRead}, r[:]...),
	}
}
func NewWriteRequest(w [RfidByteSize]byte) *SocketMessage { // TODO: req/res are the same for this one
	return &SocketMessage{
		Type: websocket.BinaryMessage,
		Data: append([]byte{FirstByteWrite}, w[:]...),
	}
}
func NewSignupRequest(readerName, secret string) *SocketMessage {
	bs, _ := json.Marshal(SignupRequest{
		Name:   RfidReaderName(readerName),
		Secret: secret,
	})
	return &SocketMessage{
		Type: websocket.BinaryMessage,
		Data: append([]byte{FirstByteSignup}, bs...),
	}
}
func NewSignupResponse(clientName RfidReaderName) *SocketMessage { // TODO: do we even need this?
	return &SocketMessage{
		Type: websocket.BinaryMessage,
		Data: append([]byte{FirstByteSignup}, []byte(clientName)...),
	}
}
func NewRenewalRequest(readerName string) *SocketMessage {
	return &SocketMessage{
		Type: websocket.PingMessage, // TODO: is this ok?
		Data: append([]byte{FirstByteRenew}, []byte(readerName)...),
	}
}
func NewRenewalResponse(secret string) *SocketMessage { // TODO: USE THIS
	return &SocketMessage{
		Type: websocket.PongMessage,
		Data: append([]byte{FirstByteRenew}, []byte(secret)...),
	}
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
	if string(resp[1:]) != expName {
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
	if err != nil {
		return err
	}
	return nil
}

func (res ReceivedMsg) ProcessReadResponse() (bytesRead [RfidByteSize]byte, err error) {
	out := [RfidByteSize]byte{}
	resp, err := res.GetResponseData(websocket.BinaryMessage, FirstByteRead)
	if len(resp) != RfidByteSize {
		return out, errors.New("bad read response size")
	}
	return [RfidByteSize]byte(resp), nil
}

func (res ReceivedMsg) GetResponseData(expMsgType int, expFirstByte byte) (resultWithTypeByte []byte, err error) {
	resultWithTypeByte = nil
	msgType, msgBytes := res.MsgType, res.Bytes
	if res.Err != nil {
		err = errors.Join(errors.New("error reading response from websocket on client"), res.Err)
		return
	}
	// validate response is as expected
	resp := &SocketMessage{}
	if err = json.Unmarshal(msgBytes, resp); err != nil {
		return
	}
	if msgType == websocket.TextMessage {
		err = errors.New(string(resp.Data))
		return
	}
	if msgType != expMsgType {
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
	return resp.Data[1:], nil
}
