package neffos

import (
	"log"
	"os"
)

// Printer is what `EnableDebug` writes neffos debug messages to.
// `*log.Logger` and kataras/golog satisfy it; wrap anything else in a `PrinterFunc`.
type Printer interface {
	Printf(format string, args ...any)
}

// PrinterFunc adapts a function to the `Printer` interface:
//
//	neffos.EnableDebug(neffos.PrinterFunc(func(format string, args ...any) {
//		slog.Debug(fmt.Sprintf(format, args...))
//	}))
type PrinterFunc func(format string, args ...any)

// Printf calls f.
func (f PrinterFunc) Printf(format string, args ...any) { f(format, args...) }

var debugPrinter Printer

// EnableDebug enables debug mode and sets the printer that receives the
// debug messages. A nil "printer" prints through a new `log.Logger` on stderr
// prefixed with "| neffos |".
//
// Note that neffos, currently, uses debug mode only on the build state of the events.
// Therefore enabling the debugger has zero performance cost on up-and-running servers and clients.
//
// There is no way to disable the debug mode on serve-time.
func EnableDebug(printer Printer) {
	if debugEnabled() {
		Debugf("debug mode is already set")
		return
	}

	if printer == nil {
		logger := log.New(os.Stderr, "| neffos | ", 0)
		logger.Println("debug mode is set")
		printer = logger
	}

	debugPrinter = printer
}

func debugEnabled() bool {
	return debugPrinter != nil
}

// Debugf prints debug messages to the printer defined on `EnableDebug`.
// Runs only on debug mode.
func Debugf(format string, args ...any) {
	if !debugEnabled() {
		return
	}

	if len(args) == 1 {
		// handles:
		// Debugf("format", func() dargs {
		//    time-consumed action that should run only on debug.
		// })
		if onDebugWithArgs, ok := args[0].(func() dargs); ok {
			args = onDebugWithArgs()
		}
	}

	debugPrinter.Printf(format, args...)
}

type dargs []any

// debugEach calls visitor for each entry of m. Runs only on debug mode.
func debugEach[K comparable, V any](m map[K]V, visitor func(K, V)) {
	if !debugEnabled() || visitor == nil {
		return
	}

	for k, v := range m {
		visitor(k, v)
	}
}
