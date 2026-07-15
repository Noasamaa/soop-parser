package errs

import "fmt"

// AppError is a typed API error returned to clients.
type AppError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	StatusCode int    `json:"-"`
}

func (e *AppError) Error() string {
	return e.Message
}

func New(code, message string, status int) *AppError {
	return &AppError{Code: code, Message: message, StatusCode: status}
}

func InvalidURL(msg string) *AppError {
	return New("invalid_url", msg, 400)
}

func NotLive(msg string) *AppError {
	return New("not_live", msg, 404)
}

func LoginRequired(msg string) *AppError {
	return New("login_required", msg, 401)
}

func PasswordRequired(msg string) *AppError {
	return New("password_required", msg, 403)
}

func GeoRestricted(msg string) *AppError {
	return New("geo_restricted", msg, 451)
}

func ResolveFailed(msg string) *AppError {
	return New("resolve_failed", msg, 502)
}

func WrapResolve(err error) *AppError {
	if err == nil {
		return nil
	}
	if ae, ok := err.(*AppError); ok {
		return ae
	}
	return ResolveFailed(fmt.Sprintf("解析失败: %v", err))
}
