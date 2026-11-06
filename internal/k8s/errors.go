package k8s

import (
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

func IsOptionalLookupError(err error) bool {
	return apierrors.IsNotFound(err) || apierrors.IsForbidden(err)
}

func DescribeError(operation, resource string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s %s: %w", operation, resource, err)
}
