-- v1.1: structured event details (box.exec: exit_code, tokens_saved, log_id).
ALTER TABLE events ADD COLUMN data TEXT;
