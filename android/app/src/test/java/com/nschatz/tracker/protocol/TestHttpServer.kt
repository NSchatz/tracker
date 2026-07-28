package com.nschatz.tracker.protocol

import java.io.ByteArrayOutputStream
import java.io.Closeable
import java.io.IOException
import java.io.InputStream
import java.net.InetAddress
import java.net.ServerSocket
import java.net.Socket
import java.nio.charset.StandardCharsets
import java.util.Collections

/**
 * A minimal HTTP/1.1 server on a real loopback socket, for testing [FixReporter].
 *
 * ### Why this exists rather than a mock — or `com.sun.net.httpserver`
 *
 * The claim under test is "this client speaks the protocol the tracker server already implements".
 * A mocked HTTP client cannot support that claim: it returns whatever the test hands it, so it is
 * satisfied by a correct client and a broken one alike. Only real bytes over a real socket show
 * whether the request line, the headers and the body are what `SPEC.md` says they must be.
 *
 * `com.sun.net.httpserver` would have been the obvious way to get that, and it is **not available**:
 * Android unit tests compile against `android.jar`, which carries the `java.*` classes but not the
 * JDK's `com.sun.*` ones. `java.net.ServerSocket` is in `android.jar`, so this parses the request
 * itself — about forty lines, and it makes the assertions *stronger*, because the raw request line
 * is visible rather than pre-parsed by a library.
 *
 * Deliberately simple: one connection at a time, `Connection: close` on every response (no
 * keep-alive state to get wrong), and no chunked-encoding support — [FixReporter] uses
 * fixed-length streaming, so a request arriving chunked would itself be a defect worth failing on.
 */
internal class TestHttpServer : Closeable {

    private val serverSocket = ServerSocket(0, 50, InetAddress.getLoopbackAddress())

    /** Recorded requests, in arrival order. */
    val requests: MutableList<RecordedRequest> = Collections.synchronizedList(mutableListOf())

    /** The status to answer with. Changed per test. */
    @Volatile
    var responseStatus: Int = 201

    /** The body to answer with. */
    @Volatile
    var responseBody: String = """{"status":"stored","deduped":false}"""

    @Volatile
    private var closed = false

    val baseUrl: String get() = "http://127.0.0.1:${serverSocket.localPort}"

    private val acceptThread = Thread({ acceptLoop() }, "test-http-server").apply {
        isDaemon = true
        start()
    }

    private fun acceptLoop() {
        while (!closed) {
            try {
                serverSocket.accept().use { handle(it) }
            } catch (_: IOException) {
                // The socket was closed, or a client hung up mid-request. Either way the loop's
                // exit condition is `closed`, so just go round again.
            }
        }
    }

    private fun handle(socket: Socket) {
        val input = socket.getInputStream()
        val requestLine = readLine(input) ?: return
        val parts = requestLine.split(' ')
        val method = parts.getOrElse(0) { "" }
        val target = parts.getOrElse(1) { "" }

        val headers = mutableMapOf<String, String>()
        while (true) {
            val line = readLine(input) ?: break
            if (line.isEmpty()) break
            val colon = line.indexOf(':')
            if (colon > 0) {
                // Header names are case-insensitive on the wire; normalise so assertions are stable.
                headers[line.substring(0, colon).trim().lowercase()] = line.substring(colon + 1).trim()
            }
        }

        val length = headers["content-length"]?.toIntOrNull() ?: 0
        val body = ByteArray(length)
        var read = 0
        while (read < length) {
            val n = input.read(body, read, length - read)
            if (n < 0) break
            read += n
        }

        requests.add(
            RecordedRequest(
                method = method,
                target = target,
                headers = headers.toMap(),
                body = String(body, 0, read, StandardCharsets.UTF_8),
            ),
        )

        val payload = responseBody.toByteArray(StandardCharsets.UTF_8)
        val response = ByteArrayOutputStream()
        // The real server sends `WWW-Authenticate: Bearer` on a 401 (see server.go's `unauthorized`),
        // so this one does too — otherwise the test would be exercising a response shape the client
        // will never actually meet.
        val authenticateHeader = if (responseStatus == 401) "WWW-Authenticate: Bearer\r\n" else ""
        response.write(
            (
                "HTTP/1.1 $responseStatus ${reasonFor(responseStatus)}\r\n" +
                    authenticateHeader +
                    "Content-Type: application/json\r\n" +
                    "Content-Length: ${payload.size}\r\n" +
                    "Connection: close\r\n\r\n"
                ).toByteArray(StandardCharsets.UTF_8),
        )
        response.write(payload)
        socket.getOutputStream().apply {
            write(response.toByteArray())
            flush()
        }
    }

    /**
     * Reads one CRLF-terminated line, byte at a time.
     *
     * Byte-at-a-time rather than a `BufferedReader` on purpose: a buffered reader would read ahead
     * past the header block and swallow the start of the body, so the request body would come back
     * truncated and the body assertions would fail for a reason that has nothing to do with the
     * client under test.
     */
    private fun readLine(input: InputStream): String? {
        val buffer = ByteArrayOutputStream()
        while (true) {
            val b = input.read()
            if (b < 0) return if (buffer.size() == 0) null else buffer.toString("UTF-8")
            if (b == '\n'.code) {
                val line = buffer.toString("UTF-8")
                return line.removeSuffix("\r")
            }
            buffer.write(b)
        }
    }

    private fun reasonFor(status: Int): String = when (status) {
        200 -> "OK"
        201 -> "Created"
        400 -> "Bad Request"
        401 -> "Unauthorized"
        403 -> "Forbidden"
        500 -> "Internal Server Error"
        503 -> "Service Unavailable"
        else -> "Status"
    }

    override fun close() {
        closed = true
        try {
            serverSocket.close()
        } catch (_: IOException) {
            // already closed
        }
        acceptThread.join(2_000)
    }
}

/** One request as it actually arrived on the wire. */
internal data class RecordedRequest(
    val method: String,
    /** The request target, e.g. `/v1/fixes` — path plus any query string, exactly as sent. */
    val target: String,
    /** Header names lowercased; values as sent. */
    val headers: Map<String, String>,
    val body: String,
)
