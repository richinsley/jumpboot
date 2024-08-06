import sys
import os
import generate
import models
import mlx.core as mx

def main():
    # use MLX to perform the a simple text completion
    # read the prompt as a line from the pipe
    prompt = sys.Pipe_in.readline().strip()

    mx.random.seed(90909090)
    model, tokenizer = models.load("Mistral-7B-Instruct-v0.3.Q8_0.gguf", "MaziyarPanahi/Mistral-7B-Instruct-v0.3-GGUF")
    # prompt = "Write a quicksort in Python"
    max_tokens = 1000
    temp = 0.5

    # generate text and print it
    # this will be copied to the golang stdout
    generate.generate(model, tokenizer, prompt, max_tokens, temp)

    # write the output to the pipe that we're all done
    sys.Pipe_out.write("done\n")
    
    # use the 
if __name__ == "__main__":
	main()