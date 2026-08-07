package i18n

import (
	"context"
	"net/http"
	"strings"
)

type ctxKey struct{}

// ContextWithLang returns a context carrying lang, the same value the
// middleware installs. Exported so a caller that answers a request
// outside the middleware chain, or a test, can localize consistently.
func ContextWithLang(ctx context.Context, lang string) context.Context {
	return context.WithValue(ctx, ctxKey{}, lang)
}

func LangFromContext(ctx context.Context) string {
	if lang, ok := ctx.Value(ctxKey{}).(string); ok {
		return lang
	}
	return "en"
}

func Middleware(i *I18n) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			lang := detectLang(r, i)
			ctx := context.WithValue(r.Context(), ctxKey{}, lang)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func detectLang(r *http.Request, i *I18n) string {
	if c, err := r.Cookie("lang"); err == nil && i.HasLocale(c.Value) {
		return c.Value
	}

	accept := r.Header.Get("Accept-Language")
	if accept != "" {
		for part := range strings.SplitSeq(accept, ",") {
			tag := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
			code, _, _ := strings.Cut(tag, "-")
			if i.HasLocale(code) {
				return code
			}
		}
	}

	return i.fallback
}
