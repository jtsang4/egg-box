package library_test

import "eggbox/internal/planimport"

func planimportOf(body, subID string) (*planimport.Result, error) {
	return planimport.Import([]byte(body), planimport.Options{SubID: subID})
}
