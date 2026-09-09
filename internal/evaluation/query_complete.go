package evaluation

func evaluationRecordWhere(
	filter QueryFilter,
) (string, []queryArgument) {
	where, arguments := evaluationQueryWhere(filter)
	if !filter.CompleteDetailOnly {
		return where, arguments
	}
	predicate := evaluationCompleteDetailPredicate()
	if where == "" {
		return " where " + predicate, arguments
	}
	return where + " and " + predicate, arguments
}
