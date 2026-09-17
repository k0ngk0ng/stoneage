package aiprovision

import "context"

type diagnosticProfileKey struct{}

// WithDiagnosticProfile carries a public profile ID, never an account or
// credential, across the Web login stages for log correlation.
func WithDiagnosticProfile(ctx context.Context, profileID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, diagnosticProfileKey{}, profileID)
}

func DiagnosticProfileID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(diagnosticProfileKey{}).(string)
	return id
}
