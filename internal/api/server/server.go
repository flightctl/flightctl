package server

import "reflect"

// Service is a wrapper around the generated server interface.
type Service interface {
	StrictServerInterface
}

// MessageProvider is an interface for types that can provide a message.
type MessageProvider interface {
	GetMessage() string
}

// PrintResponse prints the response message from the given response object.
// If the response object is a struct with a field named "Message" of type string,
// the function returns the value of that field. Otherwise, it returns a
// generic message.
func PrintResponse(resp interface{}) string {
	if mp, ok := resp.(MessageProvider); ok {
		return mp.GetMessage()
	}
	val := reflect.ValueOf(resp)
	if val.Kind() == reflect.Struct {
		msgField := val.FieldByName("Message")
		if msgField.IsValid() && msgField.Kind() == reflect.String {
			return msgField.String()
		}
	}
	return "unexpected response or no message available"
}
