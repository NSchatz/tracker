package com.nschatz.tracker.queue

import android.content.Context
import java.io.File

/**
 * The one [FixQueue] this process uses.
 *
 * A singleton, not a per-caller instance, and that is a correctness requirement rather than a
 * convenience: the collection service enqueues on the main thread while [FixUploadWorker] flushes on
 * a WorkManager thread, and `FixQueue`'s `@Synchronized` mutators only serialise those two if they
 * are holding the same object.
 *
 * The directory lives under `filesDir`, which is app-private storage: not world-readable, not on
 * external storage, and excluded from backup by `allowBackup="false"` in the manifest. A queued fix
 * is a coordinate belonging to a member of the family, so it gets the same treatment as the token.
 *
 * This file is the framework edge of the queue — it is the only part that needs a `Context`, and
 * therefore the only part the headless gate cannot exercise. Everything that decides anything is in
 * [FixQueue] and [QueueFlusher], which are plain JDK and fully tested.
 */
object FixQueues {

    /** Subdirectory of `filesDir` holding one file per queued fix. */
    const val DIRECTORY_NAME: String = "fix-queue"

    @Volatile
    private var instance: FixQueue? = null

    fun of(context: Context): FixQueue {
        instance?.let { return it }
        return synchronized(this) {
            instance ?: FixQueue(File(context.applicationContext.filesDir, DIRECTORY_NAME))
                .also { instance = it }
        }
    }
}
