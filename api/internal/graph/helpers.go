package graph

import (
	"github.com/google/uuid"
)

// Helper functions for GraphQL resolvers

func generateID() string {
	return uuid.New().String()
}

func stringOrDefault(s *string, def string) string {
	if s != nil {
		return *s
	}
	return def
}

func intOrDefault(i *int32, def int32) int32 {
	if i != nil {
		return *i
	}
	return def
}
