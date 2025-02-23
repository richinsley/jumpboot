import json
import chromadb
import sys
import jumpboot

def handle_command(command, data, client):
    try:
        if command == "create_collection":
            collection_name = data["name"]
            collection = client.create_collection(name=collection_name)
            return {}  # Return empty dict for success (no data)
        elif command == "add_documents":
            collection = client.get_collection(name=data["collection_name"])
            collection.add(
                documents=data["documents"],
                metadatas=data["metadatas"],
                ids=data["ids"],
            )
            return {}
        elif command == "query":
            collection = client.get_collection(name=data["collection_name"])
            results = collection.query(
                query_texts=data["query_texts"], n_results=data["n_results"]
            )
            return {"results": results}
        elif command == "get":
            collection = client.get_collection(name=data["collection_name"])
            # ChromaDB's get() method can take a list of IDs.
            results = collection.get(ids=data["ids"])
            return {"results": results}

        else:
            return {"error": f"Unknown command: {command}"}
    except Exception as e:
        return {"error": str(e)} #  Return the error message


def main():
    print("Python process started, waiting for commands...")
    client = chromadb.Client()  # In-memory ChromaDB client
    queue = jumpboot.JSONQueue(jumpboot.Pipe_in, jumpboot.Pipe_out)

    try:
        while True:
            try:
                message = queue.get(block=True, timeout=1)
            except TimeoutError:
                continue  # Just loop again
            except EOFError:
                print("Pipe closed, exiting.")
                break
            except Exception as e:
                print(f"Error reading message: {e}", file=sys.stderr)
                break

            if message is None: #this should not happen, but good practice
                continue
            command = message.get("command")
            data = message.get("data")

            if command == "exit":
                print("Received exit command, exiting.")
                response = handle_command(command, data, client) # <-- Get the response
                queue.put(response)  # <-- Send the response
                break

            response = handle_command(command, data, client)
            queue.put(response)

    except Exception as e:
        print("Error in main loop:", e, file=sys.stderr)
    finally:
        print("Python process exiting...")



if __name__ == "__main__":
    main()