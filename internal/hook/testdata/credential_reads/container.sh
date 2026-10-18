#!/usr/bin/env bash
docker exec fixture-container cat /run/secrets/OPENSEARCH_PASSWORD
