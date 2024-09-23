from tkinter import *
import jumpboot
import sys

import json
import os
import base64
from datetime import datetime, date
import tkinter as tk
from tkinter import ttk
import threading

queue = jumpboot.JSONQueue(jumpboot.Pipe_in, jumpboot.Pipe_out)
root = Tk()

def event_to_dict(event):
    """Convert a tkinter.Event object to a dictionary."""
    return {
        'type': event.type,
        'widget': str(event.widget),
        'x': event.x,
        'y': event.y,
        'char': event.char,
        'keysym': event.keysym,
        'keycode': event.keycode,
        'num': event.num,
        'width': event.width,
        'height': event.height,
        'delta': event.delta
    }

# Create Title
root.title(  "Paint App ")

# specify size
root.geometry("500x350")


# define function when  
# mouse double click is enabled
def paint( event ):
    # serialize the event object to json
    # and write it to the queue
    queue.put(event_to_dict(event))

    # Co-ordinates.
    x1, y1, x2, y2 = ( event.x - 3 ),( event.y - 3 ), ( event.x + 3 ),( event.y + 3 ) 
        
    # Colour
    Colour = "#000fff000"
        
    # specify type of display
    w.create_line( x1, y1, x2, 
                    y2, fill = Colour )


# create canvas widget.
w = Canvas(root, width = 400, height = 250) 

# call function when double 
# click is enabled.
w.bind( "<B1-Motion>", paint )

# create label.
l = Label( root, text = "Double Click and Drag to draw." )
l.pack()
w.pack()

mainloop()