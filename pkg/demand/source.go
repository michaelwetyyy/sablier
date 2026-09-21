package demand

import "context"

type Source interface {
	Active(context.Context) (bool, error)
}

type NamedSource struct {
	Config SourceConfig
	Source Source
}
