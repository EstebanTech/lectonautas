DROP TRIGGER IF EXISTS book_genres_max_per_book ON content.book_genres;
DROP FUNCTION IF EXISTS content.enforce_book_genre_limit();
DROP TABLE IF EXISTS content.book_genres;
DROP TABLE IF EXISTS content.genres;
