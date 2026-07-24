package encryption

import (
	"context"
	"fmt"
	"reflect"

	"gorm.io/gorm"
)

// EncryptFunc is the function signature for processing field encryption.
// Call this function for EVERY field that should be encrypted, regardless of current state.
// It handles:
// - Plaintext: encrypts
// - Already encrypted with old version: decrypts and re-encrypts
// - Already encrypted with current version: returns as-is
type EncryptFunc func(ctx context.Context, data []byte) ([]byte, error)

// ModelEncryptHandler knows how to encrypt a specific model type.
// It receives the model instance and an encryption function to use.
type ModelEncryptHandler func(ctx context.Context, model any, encrypt EncryptFunc) error

// Plugin is a GORM plugin that delegates model-specific encryption
// to handlers registered from the store/model package.
type Plugin struct {
	manager  *Manager
	handlers map[string]ModelEncryptHandler
}

// NewPlugin creates a new GORM encryption plugin with model-specific handlers.
func NewPlugin(manager *Manager, handlers map[string]ModelEncryptHandler) *Plugin {
	return &Plugin{
		manager:  manager,
		handlers: handlers,
	}
}

// Name returns the plugin name for GORM registration.
func (p *Plugin) Name() string {
	return "encryption"
}

// Initialize registers the plugin's callbacks with GORM.
func (p *Plugin) Initialize(db *gorm.DB) error {
	// Fail early if manager is nil to prevent panic in beforeSave callback
	if p.manager == nil {
		return fmt.Errorf("encryption manager is nil - plugin cannot be initialized without a valid manager")
	}

	// Register BeforeSave callback
	if err := db.Callback().Create().Before("gorm:create").Register("encryption:before_create", p.beforeSave); err != nil {
		return fmt.Errorf("register encryption:before_create callback: %w", err)
	}

	if err := db.Callback().Update().Before("gorm:update").Register("encryption:before_update", p.beforeSave); err != nil {
		return fmt.Errorf("register encryption:before_update callback: %w", err)
	}

	return nil
}

// beforeSave is the GORM callback that delegates to model-specific encryption handlers.
func (p *Plugin) beforeSave(tx *gorm.DB) {
	if tx.Error != nil || tx.Statement.Schema == nil {
		return
	}

	// Check if we have a handler for this model type
	handler, exists := p.handlers[tx.Statement.Schema.Name]
	if !exists {
		return // No encryption for this model type
	}

	ctx := tx.Statement.Context
	if ctx == nil {
		ctx = context.Background()
	}

	dest := tx.Statement.Dest
	val := reflect.ValueOf(dest)

	// Dereference pointer if needed (GORM passes *[]T for batch creates).
	if val.Kind() == reflect.Pointer {
		val = val.Elem()
	}

	// Batch operations: GORM may pass []T or []*T — encrypt each element.
	if val.Kind() == reflect.Slice {
		for i := 0; i < val.Len(); i++ {
			elem := val.Index(i)
			// For []T (non-pointer elements), take address so handler gets *T.
			if elem.Kind() != reflect.Ptr && elem.CanAddr() {
				elem = elem.Addr()
			}
			if err := handler(ctx, elem.Interface(), p.manager.ProcessEncryption); err != nil {
				tx.Logger.Error(ctx, err.Error())
				_ = tx.AddError(fmt.Errorf("encrypt model %s: %w", tx.Statement.Schema.Name, err))
				return
			}
		}
		return
	}

	if err := handler(ctx, dest, p.manager.ProcessEncryption); err != nil {
		tx.Logger.Error(ctx, err.Error())
		_ = tx.AddError(fmt.Errorf("encrypt model %s: %w", tx.Statement.Schema.Name, err))
	}
}
