package sosreport

import (
	"mime/multipart"
	"sync"

	"github.com/google/uuid"
)

type Data struct {
	ErrChan chan error
	RcvChan chan *multipart.Reader
}

type sessions sync.Map

var Sessions sessions

func (s *sessions) Add(id uuid.UUID, rcvChan chan *multipart.Reader, errChan chan error) {
	(*sync.Map)(s).Store(id.String(), &Data{
		ErrChan: errChan,
		RcvChan: rcvChan,
	})
}

func (s *sessions) Remove(id uuid.UUID) {
	(*sync.Map)(s).Delete(id.String())
}

func (s *sessions) Get(id uuid.UUID) (*Data, bool) {
	value, exists := (*sync.Map)(s).Load(id.String())
	if !exists {
		return nil, false
	}
	ret, ok := value.(*Data)
	return ret, ok
}
