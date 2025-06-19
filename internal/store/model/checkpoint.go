package model

import (
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"
)

type Checkpoint struct {
	Consumer  string `gorm:"primaryKey"`
	Key       string `gorm:"primaryKey"`
	Value     []byte
	CreatedAt time.Time `selector:"metadata.creationTimestamp"`
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

func (cp Checkpoint) String() string {
	val, err := json.Marshal(cp)
	if err != nil {
		return fmt.Sprintf("Checkpoint<marshal-error:%v>", err)
	}
	return string(val)
}
