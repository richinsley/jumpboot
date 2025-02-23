import cv2
import numpy as np
import jumpboot
import sys
from multiprocessing import shared_memory  # Correct import

def process_frame(frame, mode):
    if mode == "edges":
        gray = cv2.cvtColor(frame, cv2.COLOR_BGR2GRAY)
        edges = cv2.Canny(gray, 100, 200)
        return cv2.cvtColor(edges, cv2.COLOR_GRAY2BGR)  # Back to BGR for display
    elif mode == "blur":
        return cv2.GaussianBlur(frame, (15, 15), 0)
    elif mode == "gray":
        gray = cv2.cvtColor(frame, cv2.COLOR_BGR2GRAY)
        return cv2.cvtColor(gray, cv2.COLOR_GRAY2BGR) # Back to BGR
    else:
        return frame  # Return original frame if mode is unknown

def main():
    print("Python process started...", flush=True)
    queue = jumpboot.JSONQueue(jumpboot.Pipe_in, jumpboot.Pipe_out)
    cap = None # Initialize cap
    try:
        cap = cv2.VideoCapture(0)
        if not cap.isOpened():
            print("Error: Could not open camera", file=sys.stderr, flush=True)
            return  # Exit if camera cannot be opened

        cap.set(cv2.CAP_PROP_FRAME_WIDTH, 640)
        cap.set(cv2.CAP_PROP_FRAME_HEIGHT, 480)

        # --- Shared Memory (Python Side) ---
        # Open the *existing* shared memory segment.
        frame_shm = shared_memory.SharedMemory(name=jumpboot.SHARED_MEMORY_NAME)

        # Create a NumPy array that *views* the shared memory.  Correct shape and dtype.
        frame_array = np.ndarray((480, 640, 4), dtype=np.uint8, buffer=frame_shm.buf)

        processing_mode = "edges"

        while True:
            # --- Command Handling  ---
            try:
                message = queue.get(block=False, timeout=5)
                if message:
                    command = message.get("command")
                    if command == "set_mode":
                        processing_mode = message["data"]
                        response = {"status": "ok"}
                        queue.put(response)
                    elif command == "capture_frame":
                        # --- Frame Processing ---
                        ret, frame = cap.read()
                        if not ret:
                            response = {"status": "Error: Could not read frame"}
                            queue.put(response)
                            print("Error: Could not read frame", file=sys.stderr, flush=True)
                            continue  # Skip processing

                        processed_frame = process_frame(frame, processing_mode)

                        # --- Convert BGR to RGBA *before* writing to shared memory ---
                        processed_frame = cv2.cvtColor(processed_frame, cv2.COLOR_BGR2RGBA)

                        # --- Copy *into* shared memory ---
                        frame_array[:] = processed_frame

                        # --- Return a response so Go knows the frame is ready ---
                        response = {"status": "ok"}
                        queue.put(response)
                    elif command == "howdy":
                        response = {"status": "howdy"}
                        queue.put(response)
                    elif command == "exit":
                        response = {"status": "ok"}
                        queue.put(response)
                        break  # Exit loop
            except:  # queue.get will throw an exception if empty.
                pass

    except Exception as e:
        print(f"Python error: {e}", file=sys.stderr, flush=True)

    finally:
        if cap is not None: # important to check if cap was initialized
            cap.release()
        if 'frame_shm' in locals():  # Check if it was created before closing
           # close our shared memory segment
           frame_shm.close()

        print("Python process exiting...", flush=True)
        
if __name__ == "__main__":
    main()