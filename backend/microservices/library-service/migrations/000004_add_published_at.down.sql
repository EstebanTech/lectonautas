DROP INDEX IF EXISTS content.books_published_at_idx;
ALTER TABLE content.chapters DROP COLUMN IF EXISTS published_at;
ALTER TABLE content.books DROP COLUMN IF EXISTS published_at;
