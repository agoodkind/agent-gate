#!/usr/bin/env bash
curl --user "${OPENSEARCH_USERNAME}:${OPENSEARCH_PASSWORD}" "${OPENSEARCH_ENDPOINT}/_cluster/health"
