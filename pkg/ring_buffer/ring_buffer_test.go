package ring_buffer

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRingBuffer_PushAndPop(t *testing.T) {
	rb := NewRingBuffer[int](3)

	// Push elements
	require.NoError(t, rb.Push(1))
	require.NoError(t, rb.Push(2))
	require.NoError(t, rb.Push(3))

	// Pop elements
	val, err := rb.Pop()
	require.NoError(t, err)
	assert.Equal(t, 1, val)

	val, err = rb.Pop()
	require.NoError(t, err)
	assert.Equal(t, 2, val)

	val, err = rb.Pop()
	require.NoError(t, err)
	assert.Equal(t, 3, val)
}

func TestRingBuffer_Overwrite(t *testing.T) {
	rb := NewRingBuffer[int](2)

	// Push elements
	require.NoError(t, rb.Push(1))
	require.NoError(t, rb.Push(2))

	// Overwrite oldest element
	require.NoError(t, rb.Push(3))

	// Pop elements
	val, err := rb.Pop()
	require.NoError(t, err)
	assert.Equal(t, 2, val)

	val, err = rb.Pop()
	require.NoError(t, err)
	assert.Equal(t, 3, val)
}

func TestRingBuffer_TryPop(t *testing.T) {
	rb := NewRingBuffer[int](2)

	// Try to pop from empty buffer
	val, ok, err := rb.TryPop()
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, 0, val)

	// Push and pop
	require.NoError(t, rb.Push(1))
	val, ok, err = rb.TryPop()
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, 1, val)
}

func TestRingBuffer_Stop(t *testing.T) {
	rb := NewRingBuffer[int](2)

	// Stop the buffer
	rb.Stop()

	// Push should fail
	err := rb.Push(1)
	assert.Error(t, err)

	// Pop should fail
	_, err = rb.Pop()
	assert.Error(t, err)
}

func TestRingBuffer_Concurrency(t *testing.T) {
	rb := NewRingBuffer[int](11)
	var wg sync.WaitGroup

	// Producer
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			_ = rb.Push(i)
			time.Sleep(10 * time.Millisecond)
		}
	}()

	// Consumer
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			_, err := rb.Pop()
			require.NoError(t, err)
			time.Sleep(20 * time.Millisecond)
		}
	}()

	wg.Wait()
}

func TestRingBuffer_TryPopBehavior(t *testing.T) {
	rb := NewRingBuffer[int](3)

	// Try to pop from an empty buffer
	val, ok, err := rb.TryPop()
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, 0, val)

	// Push elements
	require.NoError(t, rb.Push(1))
	require.NoError(t, rb.Push(2))

	// Try to pop when buffer has elements
	val, ok, err = rb.TryPop()
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, 1, val)

	val, ok, err = rb.TryPop()
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, 2, val)

	// Try to pop again when buffer is empty
	val, ok, err = rb.TryPop()
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, 0, val)

	// Stop the buffer and ensure TryPop returns an error
	rb.Stop()
	val, ok, err = rb.TryPop()
	assert.Error(t, err)
	assert.False(t, ok)
	assert.Equal(t, 0, val)
}
