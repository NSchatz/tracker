package com.nschatz.tracker.alert

/**
 * A small, strict JSON reader for the two server responses the alert surface consumes.
 *
 * ### Why this exists rather than a library
 *
 * `org.json` ships in `android.jar`, but the `android.jar` on the **unit-test** classpath is the
 * stub jar: every method throws `"Stub!"` unless the module turns on `returnDefaultValues`, which
 * would make the parser return nulls in tests and prove nothing. The whole reason the client's
 * decisions live in pure Kotlin is that they are provable in a headless gate, and a parser that can
 * only run on a device would take the crossings list back out of that gate.
 *
 * A JSON library is not available either: this phase is authorised exactly ONE new external
 * dependency (the FCM client), so a second one would be a decision this change is not allowed to
 * make.
 *
 * The rest of the client already hand-rolls its JSON in both directions (`Fix.toJsonBody`,
 * `extractJsonStringField`), so this is the same trade one layer up: about 150 lines, exactly
 * testable, and no dependency.
 *
 * ### Strict, and what that means
 *
 * It parses the JSON subset the server actually emits (objects, arrays, strings, numbers, `true`,
 * `false`, `null`) and REFUSES anything else, including trailing data after the top-level value. A
 * response it cannot fully account for is refused rather than partially believed, which is the same
 * stance `/v1/fixes` takes on the way in. A refusal surfaces as a server-error state in the app,
 * never as an empty list: "we could not read the answer" and "your family has no crossings" must
 * never look the same.
 */
internal object MiniJson {

    /** Thrown when the text is not JSON this reader accepts. Carries the offset for diagnosis. */
    class MalformedJson(message: String) : Exception(message)

    /**
     * Parses one complete JSON document.
     *
     * @return `Map<String, Any?>`, `List<Any?>`, `String`, `Double`, `Boolean` or null.
     * @throws MalformedJson on anything else, including trailing content.
     */
    fun parse(text: String): Any? {
        val reader = Reader(text)
        reader.skipWhitespace()
        val value = reader.readValue(0)
        reader.skipWhitespace()
        if (!reader.atEnd()) {
            throw MalformedJson("trailing content at offset ${reader.pos}")
        }
        return value
    }

    /** Nesting depth cap. A deeply nested document is a stack overflow waiting to happen. */
    private const val MAX_DEPTH = 32

    /**
     * The form feed, U+000C. Kotlin has no `\f` escape, and a raw control character in source is an
     * invisible edit waiting to happen, so it is spelled by its code point.
     */
    private val FORM_FEED: Char = 0x0C.toChar()

    private class Reader(private val text: String) {
        var pos: Int = 0

        fun atEnd(): Boolean = pos >= text.length

        fun skipWhitespace() {
            while (pos < text.length &&
                (text[pos] == ' ' || text[pos] == '\t' || text[pos] == '\n' || text[pos] == '\r')
            ) {
                pos++
            }
        }

        fun readValue(depth: Int): Any? {
            if (depth > MAX_DEPTH) throw MalformedJson("nested deeper than $MAX_DEPTH at offset $pos")
            if (atEnd()) throw MalformedJson("unexpected end of input")
            return when (text[pos]) {
                '{' -> readObject(depth)
                '[' -> readArray(depth)
                '"' -> readString()
                't' -> readLiteral("true", true)
                'f' -> readLiteral("false", false)
                'n' -> readLiteral("null", null)
                else -> readNumber()
            }
        }

        private fun readObject(depth: Int): Map<String, Any?> {
            expect('{')
            val out = LinkedHashMap<String, Any?>()
            skipWhitespace()
            if (peek() == '}') {
                pos++
                return out
            }
            while (true) {
                skipWhitespace()
                val key = readString()
                skipWhitespace()
                expect(':')
                skipWhitespace()
                out[key] = readValue(depth + 1)
                skipWhitespace()
                when (peek()) {
                    ',' -> pos++
                    '}' -> {
                        pos++
                        return out
                    }

                    else -> throw MalformedJson("expected ',' or '}' at offset $pos")
                }
            }
        }

        private fun readArray(depth: Int): List<Any?> {
            expect('[')
            val out = mutableListOf<Any?>()
            skipWhitespace()
            if (peek() == ']') {
                pos++
                return out
            }
            while (true) {
                skipWhitespace()
                out.add(readValue(depth + 1))
                skipWhitespace()
                when (peek()) {
                    ',' -> pos++
                    ']' -> {
                        pos++
                        return out
                    }

                    else -> throw MalformedJson("expected ',' or ']' at offset $pos")
                }
            }
        }

        private fun readString(): String {
            expect('"')
            val sb = StringBuilder()
            while (true) {
                if (atEnd()) throw MalformedJson("unterminated string")
                when (val c = text[pos]) {
                    '"' -> {
                        pos++
                        return sb.toString()
                    }

                    '\\' -> {
                        pos++
                        readEscapeInto(sb)
                    }

                    else -> {
                        if (c.code < 0x20) throw MalformedJson("raw control character in string at offset $pos")
                        sb.append(c)
                        pos++
                    }
                }
            }
        }

        private fun readEscapeInto(sb: StringBuilder) {
            if (atEnd()) throw MalformedJson("unterminated escape")
            when (val esc = text[pos]) {
                '"' -> sb.append('"')
                '\\' -> sb.append('\\')
                '/' -> sb.append('/')
                'b' -> sb.append('\b')
                'f' -> sb.append(FORM_FEED)
                'n' -> sb.append('\n')
                'r' -> sb.append('\r')
                't' -> sb.append('\t')
                'u' -> {
                    if (pos + 4 >= text.length) throw MalformedJson("truncated unicode escape at offset $pos")
                    val hex = text.substring(pos + 1, pos + 5)
                    val code = hex.toIntOrNull(16)
                        ?: throw MalformedJson("bad unicode escape $hex at offset $pos")
                    sb.append(code.toChar())
                    pos += 4
                }

                else -> throw MalformedJson("bad escape $esc at offset $pos")
            }
            pos++
        }

        private fun readNumber(): Double {
            val start = pos
            if (peek() == '-') pos++
            while (!atEnd() &&
                (
                    text[pos].isDigit() || text[pos] == '.' || text[pos] == 'e' || text[pos] == 'E' ||
                        text[pos] == '+' || text[pos] == '-'
                    )
            ) {
                pos++
            }
            if (start == pos) throw MalformedJson("expected a value at offset $pos")
            return text.substring(start, pos).toDoubleOrNull()
                ?: throw MalformedJson("bad number ${text.substring(start, pos)} at offset $start")
        }

        private fun <T> readLiteral(literal: String, value: T): T {
            if (!text.startsWith(literal, pos)) throw MalformedJson("expected $literal at offset $pos")
            pos += literal.length
            return value
        }

        private fun peek(): Char {
            if (atEnd()) throw MalformedJson("unexpected end of input")
            return text[pos]
        }

        private fun expect(c: Char) {
            if (atEnd() || text[pos] != c) throw MalformedJson("expected '$c' at offset $pos")
            pos++
        }
    }
}
