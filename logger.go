package neffos

type logger interface {
	// 调试日志
	Debug(msg string)

	// 提示
	Info(msg string)

	// 警告
	Warn(msg string)

	// 错误日志
	Error(err error)
}
