package evaluation

func evaluationRecordWhere(
	filter QueryFilter,
	hasOutcome bool,
	hasSplitDetail bool,
) (string, []queryArgument) {
	where, arguments := evaluationQueryWhere(filter, hasOutcome, hasSplitDetail)
	if !filter.CompleteDetailOnly {
		return where, arguments
	}
	predicate := evaluationCompleteDetailPredicate(hasSplitDetail)
	if where == "" {
		return " where " + predicate, arguments
	}
	return where + " and " + predicate, arguments
}
