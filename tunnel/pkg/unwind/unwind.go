package unwind

// NewHook returns a new unwind hook instance with the specified handler
// The handler takes the unwind reason as parameter.
// If reason is nil, the unwind is caused by goexit, otherwise, by a panic.
func NewHook(h Handler) *Hook {
	return &Hook{
		handler: h,
	}
}

// Hook is an unwind hook that can be used to detect and handle stack unwinding caused by panic or goexit
type Hook struct {
	handler Handler
	unarmed bool
}

// Trigger activates the unwind hook
// It should be called in a defer statement
func (h *Hook) Trigger() {
	if h.unarmed {
		return
	}
	h.handler(recover())
}

// Unarm disables the unwind hook so that it will not trigger
func (h *Hook) Unarm() {
	h.unarmed = true
}

type Handler = func(reason interface{})

func WithHandler(h Handler) UnwindContext {
	return UnwindContext(h)
}

type UnwindContext Handler

func (uc UnwindContext) Do(fn func()) {
	uh := NewHook(uc)
	defer uh.Trigger()
	fn()
	uh.Unarm()
}

func (uc UnwindContext) DoError(fn func() error) error {
	uh := NewHook(uc)
	defer uh.Trigger()
	err := fn()
	uh.Unarm()
	return err
}
