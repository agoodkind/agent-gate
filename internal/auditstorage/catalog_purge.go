package auditstorage

import "slices"

func (catalog *Catalog) purgePaths() ([]string, error) {
	paths := []string{catalog.options.StatePath}
	members, err := familyPaths(catalog.options.BasePath)
	if err != nil {
		return nil, err
	}
	for _, member := range members {
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			paths = append(paths, member+suffix)
		}
		paths = append(paths, catalog.bucketLock(member).Path())
	}
	slices.Sort(paths)
	return slices.Compact(paths), nil
}
