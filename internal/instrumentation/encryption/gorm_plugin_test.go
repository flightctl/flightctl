package encryption

import (
	"context"
	"crypto/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Test models
type TestUser struct {
	ID       uint
	Name     string
	Password string
	Email    string
}

type TestRepository struct {
	ID    uint
	Name  string
	Token string
}

func TestPlugin_Name(t *testing.T) {
	mgr := createTestManager(t)
	plugin := NewPlugin(mgr, map[string]ModelEncryptHandler{})

	assert.Equal(t, "encryption", plugin.Name())
}

func TestPlugin_Initialize_Success(t *testing.T) {
	mgr := createTestManager(t)
	handlers := map[string]ModelEncryptHandler{
		"TestUser": func(ctx context.Context, v interface{}, encrypt EncryptFunc) error {
			return nil
		},
	}

	plugin := NewPlugin(mgr, handlers)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	err = plugin.Initialize(db)
	require.NoError(t, err)
}

func TestPlugin_Initialize_NilManager(t *testing.T) {
	handlers := map[string]ModelEncryptHandler{
		"TestUser": func(ctx context.Context, v interface{}, encrypt EncryptFunc) error {
			return nil
		},
	}

	plugin := NewPlugin(nil, handlers)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	// Initialize should fail with nil manager
	err = plugin.Initialize(db)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "encryption manager is nil")
}

func TestPlugin_HandlerCalled_OnCreate(t *testing.T) {
	mgr := createTestManager(t)

	handlerCalled := false
	handlers := map[string]ModelEncryptHandler{
		"TestUser": func(ctx context.Context, v interface{}, encrypt EncryptFunc) error {
			handlerCalled = true
			user := v.(*TestUser)
			if user.Password != "" {
				encrypted, err := encrypt(ctx, []byte(user.Password))
				if err != nil {
					return err
				}
				user.Password = string(encrypted)
			}
			return nil
		},
	}

	db := setupTestDB(t, mgr, handlers)

	user := TestUser{
		Name:     "Alice",
		Password: "secret123",
		Email:    "alice@example.com",
	}

	result := db.Create(&user)
	require.NoError(t, result.Error)

	assert.True(t, handlerCalled, "Handler should have been called")
	assert.True(t, strings.HasPrefix(user.Password, "enc:v1:default:"), "Password should be encrypted")
}

func TestPlugin_HandlerCalled_OnUpdate(t *testing.T) {
	mgr := createTestManager(t)

	callCount := 0
	handlers := map[string]ModelEncryptHandler{
		"TestUser": func(ctx context.Context, v interface{}, encrypt EncryptFunc) error {
			callCount++
			user := v.(*TestUser)
			if user.Password != "" {
				encrypted, err := encrypt(ctx, []byte(user.Password))
				if err != nil {
					return err
				}
				user.Password = string(encrypted)
			}
			return nil
		},
	}

	db := setupTestDB(t, mgr, handlers)

	user := TestUser{
		Name:     "Bob",
		Password: "oldpass",
		Email:    "bob@example.com",
	}

	// Create (handler called once)
	db.Create(&user)
	assert.Equal(t, 1, callCount)

	// Update (handler called again)
	user.Password = "newpass"
	result := db.Save(&user)
	require.NoError(t, result.Error)

	assert.Equal(t, 2, callCount, "Handler should be called on both Create and Update")
	assert.True(t, strings.HasPrefix(user.Password, "enc:v1:default:"))
}

func TestPlugin_NoHandler_SkipsEncryption(t *testing.T) {
	mgr := createTestManager(t)

	// No handlers registered
	handlers := map[string]ModelEncryptHandler{}

	db := setupTestDB(t, mgr, handlers)

	user := TestUser{
		Name:     "Charlie",
		Password: "plaintext",
		Email:    "charlie@example.com",
	}

	result := db.Create(&user)
	require.NoError(t, result.Error)

	// Password should remain plaintext (no handler to encrypt it)
	assert.Equal(t, "plaintext", user.Password)
}

func TestPlugin_HandlerError_PropagatesError(t *testing.T) {
	mgr := createTestManager(t)

	handlers := map[string]ModelEncryptHandler{
		"TestUser": func(ctx context.Context, v interface{}, encrypt EncryptFunc) error {
			return assert.AnError // Simulate handler error
		},
	}

	db := setupTestDB(t, mgr, handlers)

	user := TestUser{
		Name:     "Dave",
		Password: "test",
		Email:    "dave@example.com",
	}

	result := db.Create(&user)
	assert.Error(t, result.Error, "Handler error should propagate to GORM")
	assert.Contains(t, result.Error.Error(), "encrypt model TestUser")
}

func TestPlugin_MultipleModels_CorrectHandler(t *testing.T) {
	mgr := createTestManager(t)

	userHandlerCalled := false
	repoHandlerCalled := false

	handlers := map[string]ModelEncryptHandler{
		"TestUser": func(ctx context.Context, v interface{}, encrypt EncryptFunc) error {
			userHandlerCalled = true
			return nil
		},
		"TestRepository": func(ctx context.Context, v interface{}, encrypt EncryptFunc) error {
			repoHandlerCalled = true
			return nil
		},
	}

	db := setupTestDB(t, mgr, handlers)
	_ = db.AutoMigrate(&TestRepository{})

	// Create user - should call user handler
	user := TestUser{Name: "Alice"}
	db.Create(&user)
	assert.True(t, userHandlerCalled)
	assert.False(t, repoHandlerCalled)

	// Reset flags
	userHandlerCalled = false
	repoHandlerCalled = false

	// Create repo - should call repo handler
	repo := TestRepository{Name: "myrepo"}
	db.Create(&repo)
	assert.False(t, userHandlerCalled)
	assert.True(t, repoHandlerCalled)
}

func TestPlugin_UsesProcessEncryption(t *testing.T) {
	mgr := createTestManager(t)

	var encryptFuncReceived EncryptFunc

	handlers := map[string]ModelEncryptHandler{
		"TestUser": func(ctx context.Context, v interface{}, encrypt EncryptFunc) error {
			encryptFuncReceived = encrypt
			return nil
		},
	}

	db := setupTestDB(t, mgr, handlers)

	user := TestUser{Name: "Test"}
	db.Create(&user)

	require.NotNil(t, encryptFuncReceived)

	// Test that it's ProcessEncryption by checking it handles already-encrypted data
	plaintext := []byte("test-data")
	encrypted, err := mgr.Encrypt(context.Background(), plaintext)
	require.NoError(t, err)

	// ProcessEncryption should preserve already-encrypted data with same key
	result, err := encryptFuncReceived(context.Background(), encrypted)
	require.NoError(t, err)
	assert.Equal(t, encrypted, result, "ProcessEncryption should preserve already-encrypted data")
}

func TestPlugin_BatchCreate_HandlerCalledForSlice(t *testing.T) {
	mgr := createTestManager(t)

	var receivedType string

	handlers := map[string]ModelEncryptHandler{
		"TestUser": func(ctx context.Context, v interface{}, encrypt EncryptFunc) error {
			switch val := v.(type) {
			case *TestUser:
				receivedType = "single"
			case *[]TestUser:
				receivedType = "slice"
				// Encrypt each user
				for i := range *val {
					if (*val)[i].Password != "" {
						encrypted, err := encrypt(ctx, []byte((*val)[i].Password))
						if err != nil {
							return err
						}
						(*val)[i].Password = string(encrypted)
					}
				}
			}
			return nil
		},
	}

	db := setupTestDB(t, mgr, handlers)

	users := []TestUser{
		{Name: "User1", Password: "pass1"},
		{Name: "User2", Password: "pass2"},
	}

	result := db.Create(&users)
	require.NoError(t, result.Error)

	assert.Equal(t, "slice", receivedType, "Handler should receive *[]TestUser for batch create")

	// Verify both passwords encrypted
	assert.True(t, strings.HasPrefix(users[0].Password, "enc:"))
	assert.True(t, strings.HasPrefix(users[1].Password, "enc:"))
}

func TestPlugin_NilContext_UsesBackground(t *testing.T) {
	mgr := createTestManager(t)

	var ctxReceived context.Context

	handlers := map[string]ModelEncryptHandler{
		"TestUser": func(ctx context.Context, v interface{}, encrypt EncryptFunc) error {
			ctxReceived = ctx
			return nil
		},
	}

	db := setupTestDB(t, mgr, handlers)

	// Create without explicit context
	user := TestUser{Name: "Test"}
	db.Create(&user)

	assert.NotNil(t, ctxReceived, "Handler should receive a context even if Statement.Context is nil")
}

// Helper functions

func createTestManager(t *testing.T) *Manager {
	t.Helper()
	var err error
	manager := NewManager()

	key := make([]byte, 32)
	_, err = rand.Read(key)
	require.NoError(t, err)

	v1 := newV1Strategy()
	err = v1.AddKey("default", key, true)
	require.NoError(t, err)

	manager.RegisterStrategy(v1, true)

	return manager
}

func setupTestDB(t *testing.T, manager *Manager, handlers map[string]ModelEncryptHandler) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	err = db.Use(NewPlugin(manager, handlers))
	require.NoError(t, err)

	err = db.AutoMigrate(&TestUser{})
	require.NoError(t, err)

	return db
}
