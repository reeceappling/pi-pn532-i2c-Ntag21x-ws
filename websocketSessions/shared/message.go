package shared

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gorilla/websocket"
	"time"
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

func (sockMsg *SocketMessage) ValidateReadRequest() error {
	if sockMsg.Type != ReadRequestType {
		return errors.New("invalid type on read request")
	}
	if len(sockMsg.Data) != 1 {
		return errors.New("read request must contain only 1 byte")
	}
	if sockMsg.Data[0] != FirstByteRead {
		return errors.New("incorrect first byte on read request")
	}
	return nil
}
func (sockMsg *SocketMessage) ValidateWriteRequest() ([RfidByteSize]byte, error) {
	if sockMsg.Type != WriteRequestType {
		return [RfidByteSize]byte{}, errors.New("invalid type on write request")
	}
	if len(sockMsg.Data) != 1+RfidByteSize {
		return [RfidByteSize]byte{}, errors.New("invalidly sized write request")
	}
	if sockMsg.Data[0] != WriteRequestType {
		return [RfidByteSize]byte{}, errors.New("incorrect first byte on write request")
	}
	result := [RfidByteSize]byte(sockMsg.Data[1:]) // TODO: ensure works
	return result, nil
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
	err := c.WriteMessage(sockMsg.Type, sockMsg.Data)
	if err != nil {
		return errors.Join(errors.New("failed to write socket message to connection"), err)
	}
	return nil
}

const (
	FirstByteRead uint8 = iota
	FirstByteWrite
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

const ReadRequestType = websocket.BinaryMessage
const ReadResponseType = websocket.BinaryMessage
const WriteRequestType = websocket.BinaryMessage
const WriteResponseType = websocket.BinaryMessage

func NewReadRequest() *SocketMessage {
	return &SocketMessage{
		Type: ReadRequestType,
		Data: []byte{FirstByteRead},
	}
}
func NewReadResponse(r [RfidByteSize]byte) *SocketMessage {
	return &SocketMessage{
		Type: ReadResponseType,
		Data: r[:],
	}
}
func NewWriteRequest(w [RfidByteSize]byte) *SocketMessage {
	bs := make([]byte, 0, RfidByteSize+1)
	bs = append(bs, FirstByteWrite)
	bs = append(bs, w[:]...)
	return &SocketMessage{
		Type: WriteRequestType,
		Data: bs,
	}
}
func NewWriteResponse(w [RfidByteSize]byte) *SocketMessage {
	return &SocketMessage{
		Type: WriteResponseType,
		Data: w[:],
	}
}

type SignupResponse struct {
	ClientName string `json:"clientName"`
}

func (resp *SignupResponse) Validate(expectedName string) error {
	if resp.ClientName != expectedName {
		return fmt.Errorf(`Expected name %s, received name %s`, expectedName, resp.ClientName)
	}
	return nil
}
func (msg *SocketMessage) ParseSignupResponse() (out *SignupResponse, err error) { // TODO: USE!
	if msg == nil {
		return nil, errors.New("Signup response socket message was nil")
	}
	if msg.Type != SignupResponseType {
		return nil, fmt.Errorf(`2 Signup response type invalid, should be text (%d), was %d`, SignupResponseType, msg.Type)
	}
	return &SignupResponse{ClientName: string(msg.Data)}, nil
}
func (msg *SocketMessage) ParseSignupRequest() (out *SignupRequest, err error) { // TODO: USE!
	if msg == nil {
		return nil, errors.New("Signup response socket message was nil")

	}
	if msg.Type != SignupRequestType {
		return nil, fmt.Errorf(`1 Signup request type invalid, should be (%d), was %d`, SignupRequestType, msg.Type)
	}
	out = &SignupRequest{}
	err = json.Unmarshal(msg.Data, out)
	return out, err
}

const SignupRequestType = websocket.BinaryMessage
const SignupResponseType = websocket.TextMessage

func NewSignupRequest(readerName, secret string) *SocketMessage {
	bs, _ := json.Marshal(SignupRequest{
		Name:   RfidReaderName(readerName),
		Secret: secret,
	})
	println("sending signup request:", string(bs))
	return &SocketMessage{
		Type: SignupRequestType, // TODO; used to be signup, should this be text?
		Data: bs,
	}
}
func NewSignupResponse(clientName RfidReaderName) *SocketMessage {
	return &SocketMessage{
		Type: SignupResponseType,
		Data: []byte(clientName),
	}
}
func NewRenewalRequest(readerName string) *SocketMessage {
	return &SocketMessage{
		Type: websocket.PingMessage, // TODO: is this ok?
		Data: []byte(readerName),
	}
}
func NewRenewalResponse(secret string) *SocketMessage {
	return &SocketMessage{
		Type: websocket.PongMessage,
		Data: []byte(secret),
	}
}

//func (res ReceivedMsg) ValidateRenewalRequest(expName string) error {
//	return res.genericValidate(websocket.PingMessage, FirstByteRenew, expName, "renewal request")
//}

//func (res ReceivedMsg) ValidateRenewalResponse(expSecret string) error {
//	return res.genericValidate(websocket.PongMessage, FirstByteRenew, expSecret, "renewal response")
//}

//func (res ReceivedMsg) genericValidate(expMsgType int, expFirstByte uint8, exStr string, what string) error {
//	resp, err := res.GetRequestData(expMsgType, expFirstByte)
//	if err != nil {
//		return err
//	}
//	if string(resp) != exStr {
//		return fmt.Errorf(`received incorrect secret on %s`, what)
//	}
//	return nil
//}

func TryGetMessage(ctx context.Context, conn *websocket.Conn, timeout ...time.Duration) ReceivedMsg {
	timeoutActual := 30 * time.Second // TODO: default ok?
	if len(timeout) > 0 {
		timeoutActual = timeout[0]
	}
	ctxWithCancel, cancel := context.WithTimeout(ctx, timeoutActual)
	defer cancel()

	resultChan := make(chan ReceivedMsg, 1)
	go func() {
		defer close(resultChan)                                // TODO: or in gofunc?
		println("attempting to read message in TryGetMessage") // TODO: del!
		msgType, bytes, err := conn.ReadMessage()              // Bytes may include first bytes to define type              // TODO: this will generally be binary or text for non-signup-flow items
		println("results:", "type:", msgType)
		if len(bytes) != 0 {
			println("content:", string(bytes))
		}
		if err != nil {
			println("err:", err.Error())
		}
		resultChan <- ReceivedMsg{msgType, bytes, err}
	}()
	println("waiting for response") // TODO: del
	select {
	case res := <-resultChan:
		return res
	case <-ctxWithCancel.Done():
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

func (res ReceivedMsg) AsSignupResponse(expName string) (out *SignupResponse, err error) { // TODO: USE!
	if res.MsgType != SignupResponseType {
		return nil, fmt.Errorf(`3 Signup response type invalid, should be %d, was %d`, SignupResponseType, res.MsgType)
	}
	if res.Err != nil {
		return nil, errors.Join(errors.New("Error on signup response"), res.Err)
	}
	name := string(res.Bytes)
	if name != expName {
		return nil, fmt.Errorf("names did not match: %s %s", name, expName)
	}

	return &SignupResponse{ClientName: name}, nil
}
func (res ReceivedMsg) SocketMsg() (out *SocketMessage, err error) { // TODO: USE!
	if res.MsgType == MessageTypeError {
		if res.Err != nil {
			return nil, errors.Join(errors.New("Error found on incoming message"), res.Err)
		}
		return nil, errors.New("Erroneous message without error attached")
	}
	if res.Err != nil {
		return nil, errors.Join(errors.New("Error on signup response without appropriate message type"), res.Err)
	}
	return &SocketMessage{
		Type: res.MsgType,
		Data: res.Bytes,
	}, nil
}
func (res ReceivedMsg) SocketMsgUnsafe() (out *SocketMessage) { // TODO: USE!
	return &SocketMessage{
		Type: res.MsgType,
		Data: res.Bytes,
	}
}
func (res ReceivedMsg) AsSignupRequest() (out *SignupRequest, err error) { // TODO: USE!
	if res.Err != nil {
		return nil, errors.Join(errors.New("error on signup response"), res.Err)
	}
	if res.MsgType != SignupRequestType {
		return nil, fmt.Errorf(`signup request type invalid, should be %d, was %d`, SignupRequestType, res.MsgType)
	}
	if res.Bytes == nil {
		err = errors.New("signup request must not have nil bytes")
		println(err.Error()) // TODO: del
		return out, err
	} else {
		if len(res.Bytes) == 0 {
			err = errors.New("signup request must not have empty bytes")
			println(err.Error()) // TODO: del
			return out, err
		} else {
			println("reqBytes " + string(res.Bytes)) // TODO: del
		}
	}
	out, err = res.SocketMsgUnsafe().ParseSignupRequest()
	if err != nil {
		return nil, errors.Join(errors.New("failed to unmarshal signup request received"), err)
	}
	return out, nil
}

const RfidByteSize = 8 // TODO: is this correct?? must match RfidByteSize in mdb.go
//func (res ReceivedMsg) ValidateWriteRequest() (toWrite [RfidByteSize]byte, err error) {
//	if res.Err != nil {
//		return [RfidByteSize]byte{}, res.Err
//	}
//	req, err := res.GetRequestData(websocket.BinaryMessage, FirstByteWrite)
//	if err != nil {
//		return [RfidByteSize]byte{}, err
//	}
//	if len(req) != RfidByteSize {
//		return [RfidByteSize]byte{}, errors.New("invalid write request size")
//	}
//	return [RfidByteSize]byte(req), nil
//}

func (res ReceivedMsg) ProcessWriteResponse(expectedWritten [RfidByteSize]byte) error {
	if res.Err != nil {
		return errors.New("error on write response: " + res.Err.Error())
	}
	if res.MsgType != websocket.BinaryMessage {
		return errors.New("invalid write response type")
	}
	if len(res.Bytes) != RfidByteSize {
		return errors.New("invalid write response size")
	}
	if string(expectedWritten[:]) != string(res.Bytes) {
		return errors.New("bad response content, wrote wrong Bytes")
	}
	return nil
	//resp, err := res.GetRequestData(websocket.BinaryMessage, FirstByteRead)
	//if len(resp) != RfidByteSize {
	//	return out, errors.New("bad read response size")
	//}
	//return [RfidByteSize]byte(resp), nil

	//resp, err := res.GetRequestData(websocket.BinaryMessage, FirstByteWrite)
	//if err != nil {
	//	return err
	//}
	//if string(expectedWritten[:]) != string(resp) {
	//	return errors.New("bad response content, wrote wrong Bytes")
	//}
	//return nil
}

func (res ReceivedMsg) ProcessReadResponse() (bytesRead [RfidByteSize]byte, err error) {
	println("processing read response: ") // TODO: del
	if res.Err != nil {
		println("error on read response: " + res.Err.Error()) // TODO: del
		return [RfidByteSize]byte{}, errors.New("error on read response: " + res.Err.Error())
	}
	if res.Bytes == nil {
		println("nil result bytes!: ") // TODO: del
		return [RfidByteSize]byte{}, errors.New("invalid read response size")
	}
	if len(res.Bytes) != RfidByteSize {
		println("bad byte size, result:" + string(res.Bytes)) // TODO: del
		return [RfidByteSize]byte{}, errors.New("invalid read response size")
	}
	if res.MsgType != ReadResponseType {
		println("bad response type:", res.MsgType, "should be", ReadResponseType) // TODO: del
		return [RfidByteSize]byte{}, errors.New("invalid read response type")
	}
	//resp, err := res.GetRequestData(websocket.BinaryMessage, FirstByteRead)
	//if len(resp) != RfidByteSize {
	//	return out, errors.New("bad read response size")
	//}
	//return [RfidByteSize]byte(resp), nil
	return [RfidByteSize]byte(res.Bytes), nil
}

//func (res ReceivedMsg) GetRequestData(expMsgType int, expFirstByte byte) (resultWithTypeByte []byte, err error) {
//	resultWithTypeByte = nil
//	msgType, msgBytes := res.MsgType, res.Bytes
//	if res.Err != nil {
//		err = errors.Join(errors.New("error reading response from websocket on client"), res.Err)
//		println("res.Err != nil", err.Error()) // TODO: del
//		return
//	}
//	resp, err := res.SocketMsg()
//	if err != nil {
//		println("got a non-socketMessage: " + err.Error())
//		return nil, err
//	}
//	// validate response is as expected
//	if msgType == websocket.TextMessage {
//		err = errors.New(string(resp.Data))
//		println("errMsg", string(resp.Data))
//		return
//	}
//	if msgType != expMsgType {
//		err = errors.New("unexpected message format for response")
//		println("unexpected message format for response", msgType, "expected", expMsgType)
//		return
//	}
//	if resp.Data[0] != expFirstByte {
//		err = fmt.Errorf(`first byte was expected to be %d, got %d`, int(expFirstByte), resp.Data[0])
//		println(err.Error()) // TODO: del
//		return
//	}
//	if len(resp.Data) == 1 { // TODO: will it ever be 0?
//		return nil, nil // TODO: ok?
//	}
//	return resp.Data[1:], nil
//}
