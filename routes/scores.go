package routes

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/code-golf/code-golf/hole"
	"github.com/code-golf/code-golf/lang"
	"github.com/code-golf/code-golf/session"
)

func scoresMini(w http.ResponseWriter, r *http.Request) {
	var userID int
	if golfer := session.Golfer(r); golfer != nil {
		userID = golfer.ID
	}

	var json []byte

	if err := session.Database(r).QueryRow(
		`WITH leaderboard AS (
		    SELECT ROW_NUMBER() OVER (ORDER BY chars, submitted),
		           RANK()       OVER (ORDER BY chars),
		           user_id,
		           chars,
		           user_id = $1 me
		      FROM solutions
		     WHERE hole = $2
		       AND lang = $3
		       AND NOT failing
		), mini_leaderboard AS (
		    SELECT rank,
		           login,
		           me,
		           chars strokes
		      FROM leaderboard
		      JOIN users on user_id = id
		     WHERE row_number >
		           COALESCE((SELECT row_number - 4 FROM leaderboard WHERE me), 0)
		  ORDER BY row_number
		     LIMIT 7
		) SELECT COALESCE(JSON_AGG(mini_leaderboard), '[]') FROM mini_leaderboard`,
		userID,
		param(r, "hole"),
		param(r, "lang"),
	).Scan(&json); err != nil {
		panic(err)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(json)
}

func scoresAll(w http.ResponseWriter, r *http.Request) {
	var json []byte

	if err := session.Database(r).QueryRow(
		`WITH solution_lengths AS (
		    SELECT hole,
		           lang,
		           login,
		           chars strokes,
		           submitted
		      FROM solutions
		      JOIN users on user_id = id
		      WHERE NOT failing
		        AND $1 IN ('all-holes', hole::text)
		        AND $2 IN ('all-langs', lang::text)
		) SELECT COALESCE(JSON_AGG(solution_lengths), '[]') FROM solution_lengths`,
		param(r, "hole"),
		param(r, "lang"),
	).Scan(&json); err != nil {
		panic(err)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(json)
}

// Scores serves GET /scores/{hole}/{lang}/{suffix}
func Scores(w http.ResponseWriter, r *http.Request) {
	holeID := param(r, "hole")
	langID := param(r, "lang")

	if _, ok := hole.ByID[holeID]; holeID != "all-holes" && !ok {
		NotFound(w, r)
		return
	}

	// Redirect legacy name for Raku.
	if langID == "perl6" {
		http.Redirect(
			w, r,
			strings.Replace(r.RequestURI, "perl6", "raku", 1),
			http.StatusPermanentRedirect,
		)
		return
	}

	if _, ok := lang.ByID[langID]; langID != "all-langs" && !ok {
		NotFound(w, r)
		return
	}

	type Score struct {
		Bytes, Chars, Holes, Points, Rank int
		Lang                              lang.Lang
		Login                             string
		Submitted                         time.Time
	}

	data := struct {
		HoleID, LangID string
		Holes          []hole.Hole
		Langs          []lang.Lang
		Next, Prev     int
		Scores         []Score
	}{
		HoleID: holeID,
		Holes:  hole.List,
		LangID: langID,
		Langs:  lang.List,
	}

	page := 1

	if suffix := param(r, "suffix"); suffix != "" {
		if suffix == "mini" {
			scoresMini(w, r)
			return
		}

		if suffix == "all" {
			scoresAll(w, r)
			return
		}

		page, _ = strconv.Atoi(suffix)

		if page < 1 {
			NotFound(w, r)
			return
		}

		if page == 1 {
			http.Redirect(w, r, "/scores/"+holeID+"/"+langID, http.StatusPermanentRedirect)
			return
		}
	}

	if page != 1 {
		data.Prev = page - 1
	}

	var distinct, table, title string

	if holeID == "all-holes" {
		distinct = "DISTINCT ON (hole, user_id)"
		table = "summed_leaderboard"
	} else {
		table = "scored_leaderboard"
	}

	rows, err := session.Database(r).Query(
		`WITH leaderboard AS (
		  SELECT `+distinct+`
		         hole,
		         submitted,
		         bytes,
		         chars,
		         user_id,
		         lang
		    FROM solutions
		   WHERE NOT failing
		     AND $1 IN ('all-holes', hole::text)
		     AND $2 IN ('all-langs', lang::text)
		ORDER BY hole, user_id, chars, submitted
		), scored_leaderboard AS (
		  SELECT hole,
		         1 holes,
		         ROUND(
		             (COUNT(*) OVER (PARTITION BY hole) -
		                RANK() OVER (PARTITION BY hole ORDER BY chars) + 1)
		             * (1000.0 / COUNT(*) OVER (PARTITION BY hole))
		         ) points,
		         bytes,
		         chars,
		         submitted,
		         user_id,
		         lang
		    FROM leaderboard
		), summed_leaderboard AS (
		  SELECT user_id,
		         COUNT(*)       holes,
		         '' lang,
		         SUM(points)    points,
		         SUM(bytes)     bytes,
		         SUM(chars)     chars,
		         MAX(submitted) submitted
		    FROM scored_leaderboard
		GROUP BY user_id
		) SELECT bytes,
		         chars,
		         holes,
		         lang,
		         login,
		         points,
		         RANK() OVER (ORDER BY points DESC, chars),
		         submitted
		    FROM `+table+`
		    JOIN users on user_id = id
		ORDER BY points DESC, chars, submitted
		   LIMIT 101
		  OFFSET $3`,
		holeID,
		langID,
		(page-1)*100,
	)
	if err != nil {
		panic(err)
	}

	defer rows.Close()

	for rows.Next() {
		// We overselect by one so we can test for a next page.
		if len(data.Scores) == 100 {
			data.Next = page + 1
			continue
		}

		var langID string
		var score Score

		if err := rows.Scan(
			&score.Bytes,
			&score.Chars,
			&score.Holes,
			&langID,
			&score.Login,
			&score.Points,
			&score.Rank,
			&score.Submitted,
		); err != nil {
			panic(err)
		}

		score.Lang = lang.ByID[langID]

		data.Scores = append(data.Scores, score)
	}

	if err := rows.Err(); err != nil {
		panic(err)
	}

	if holeID == "all-holes" {
		title = "All Holes"
	} else {
		title = hole.ByID[holeID].Name
	}

	title += " in "

	if langID == "all-langs" {
		title += "All Langs"
	} else {
		title += lang.ByID[langID].Name
	}

	render(w, r, "scores", title, data)
}
