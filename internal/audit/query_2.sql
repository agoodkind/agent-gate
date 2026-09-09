select rule, mode, field_path, file_path, start, end, message from violations where event_id = ? order by id
