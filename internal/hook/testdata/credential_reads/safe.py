from __future__ import annotations

import os

VARIABLE_NAME = "SERVICE_PASSWORD_FILE"
STATUS_TEXT = "The service accepts a credential file reference."

credential_path = os.environ[VARIABLE_NAME]
print(STATUS_TEXT)
